/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/google/go-cmp/cmp"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/providers/file"
)

var (
	fLocalKubeconfig     = flag.String("local-kubeconfig", "", "Path to the local kubeconfig file")
	fProviderKubeconfigs = flag.String("provider-kubeconfigs", "", "Comma-separated list of provider cluster kubeconfig file paths")
)

func main() {
	ctrllog.SetLogger(zap.New(zap.UseDevMode(true)))

	flag.Parse()
	ctx := signals.SetupSignalHandler()

	if err := ignoreCanceled(doMain(ctx)); err != nil {
		ctrllog.Log.Error(err, "fatal error")
	}
}

func doMain(ctx context.Context) error {
	localKubeconfig := strings.TrimSpace(*fLocalKubeconfig)
	if localKubeconfig == "" {
		return errors.New("the --local-kubeconfig flag is required")
	}

	localCfg, err := clientcmd.BuildConfigFromFlags("", localKubeconfig)
	if err != nil {
		return fmt.Errorf("error building local kubeconfig: %w", err)
	}

	kubeconfigFiles := strings.Split(*fProviderKubeconfigs, ",")
	if len(kubeconfigFiles) == 1 && kubeconfigFiles[0] == "" {
		kubeconfigFiles = []string{}
	}

	// Build a schema for provider clusters. The provider will use this
	// schema to build the clients for each cluster, hence it needs
	// every type that should be interacted with through the provider
	// clusters.
	providerScheme := runtime.NewScheme()
	if err := corev1.AddToScheme(providerScheme); err != nil {
		return fmt.Errorf("error adding corev1 to provider scheme: %w", err)
	}

	provider, err := file.New(file.Options{
		KubeconfigFiles: kubeconfigFiles,
		ClusterOptions: []cluster.Option{
			func(o *cluster.Options) {
				o.Scheme = providerScheme
			},
		},
	})
	if err != nil {
		return fmt.Errorf("error setting up file provider: %w", err)
	}

	// The manager schema needs to have all types interacted with by
	// both the local and provider clusters as this is the schema the
	// manager uses to set up watches.
	//
	// E.g. the local cluster has the CRD AppConfig and the provider
	// clusters have CRD AppInstance both types need to be in the
	// manager schema but only the AppInstance needs to be in the
	// provider schema.
	//
	// Note: If the provider clusters are not uniform the manager still
	// needs a schema that contains all types used across all provider
	// clusters. Going with the example above if AppConfig is on the
	// local cluster and AppInstance is on some provider clusters and
	// AppDatabase on other provider clusters the manager needs all
	// three (AppConfig, AppInstance, AppDatabase) in its schema while.
	// The providers for the AppInstance and AppDatabase clusters only
	// need their respective types.
	managerScheme := runtime.NewScheme()
	if err := corev1.AddToScheme(managerScheme); err != nil {
		return fmt.Errorf("error adding corev1 to manager scheme: %w", err)
	}

	mgr, err := mcmanager.New(localCfg, provider, mcmanager.Options{
		Scheme: managerScheme,
	})
	if err != nil {
		return fmt.Errorf("error setting up mcmanager: %w", err)
	}

	// A label selector predicate to only sync ConfigMaps we create.
	predicate, err := predicate.LabelSelectorPredicate(metav1.LabelSelector{
		MatchLabels: map[string]string{
			"example.io/propagate": "true",
		},
	})
	if err != nil {
		return fmt.Errorf("failed to build label selector predicate: %w", err)
	}

	// One controller that watches ConfigMaps in the local cluster and
	// propagates them to all provider clusters.
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("local").
		For(&corev1.ConfigMap{}, mcbuilder.WithEngageWithLocalCluster(true)).
		WithEventFilter(predicate).
		Complete(
			pushLocalToProvider(mgr.GetLocalManager(), provider),
		); err != nil {
		return fmt.Errorf("failed to build controller: %w", err)
	}

	// Another controller that watches ConfigMaps in provider clusters
	// and ensures they match the local cluster, reverting any local
	// changes.
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("provider").
		For(&corev1.ConfigMap{}, mcbuilder.WithEngageWithProviderClusters(true)).
		WithEventFilter(predicate).
		Complete(
			pullProviderFromLocal(mgr.GetLocalManager(), provider),
		); err != nil {
		return fmt.Errorf("failed to build controller: %w", err)
	}

	return mgr.Start(ctx)
}

func pushLocalToProvider(localCl cluster.Cluster, provider *file.Provider) mcreconcile.Func {
	return mcreconcile.Func(
		func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			log := ctrllog.FromContext(ctx).WithValues(
				"cluster", req.ClusterName,
				"namespace", req.Request.Namespace,
				"name", req.Request.Name,
			)
			log.Info("Reconciling ConfigMap")

			cm := &corev1.ConfigMap{}
			if err := localCl.GetClient().Get(ctx, req.Request.NamespacedName, cm); err != nil {
				if !apierrors.IsNotFound(err) {
					return reconcile.Result{}, err
				}
				log.Info("ConfigMap not found, deleting from provider clusters")
				// Iterate over all provider clusters and delete
				// the ConfigMap. Note that .ClusterNames is not
				// part of the multicluster provider interface.
				// Accessing this depends on the provider.
				var errs error
				for _, clusterName := range provider.ClusterNames() {
					cl, err := provider.Get(ctx, clusterName)
					if err != nil {
						return reconcile.Result{}, err
					}
					if err := cl.GetClient().Delete(ctx, &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: req.Request.Namespace,
							Name:      req.Request.Name,
						},
					}); err != nil {
						if !apierrors.IsNotFound(err) {
							errs = errors.Join(errs, err)
							continue
						}
					}
					log.Info("ConfigMap deleted in provider cluster", "provider-cluster", clusterName)
				}
				return reconcile.Result{}, err
			}
			log.Info("ConfigMap retrieved")

			// Propagate to all provider clusters
			var errs error
			for _, clusterName := range provider.ClusterNames() {
				log := log.WithValues("provider-cluster", clusterName)
				log.Info("Propagating ConfigMap to provider cluster")
				cl, err := provider.Get(ctx, clusterName)
				if err != nil {
					errs = errors.Join(errs, err)
					continue
				}

				existingCM := &corev1.ConfigMap{}
				err = cl.GetClient().Get(ctx, req.Request.NamespacedName, existingCM)
				if err != nil && !apierrors.IsNotFound(err) {
					errs = errors.Join(errs, err)
					continue
				}

				if apierrors.IsNotFound(err) {
					// Create new ConfigMap
					newCM := &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: cm.Namespace,
							Name:      cm.Name,
							Labels:    cm.Labels,
						},
						Data: cm.Data,
					}
					newCM.ResourceVersion = ""
					if err := cl.GetClient().Create(ctx, newCM); err != nil {
						errs = errors.Join(errs, err)
						continue
					}
					log.Info("ConfigMap created in provider cluster")
					continue
				}

				if cmp.Equal(existingCM.Data, cm.Data) {
					log.Info("ConfigMap in provider cluster is up-to-date")
					continue
				}

				// Update existing ConfigMap
				existingCM.Data = cm.Data
				if err := cl.GetClient().Update(ctx, existingCM); err != nil {
					errs = errors.Join(errs, err)
					continue
				}
				log.Info("ConfigMap updated in provider cluster")
			}
			return ctrl.Result{}, errs
		},
	)
}

func pullProviderFromLocal(localCl cluster.Cluster, provider *file.Provider) mcreconcile.Func {
	return mcreconcile.Func(
		func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			log := ctrllog.FromContext(ctx).WithValues(
				"cluster", req.ClusterName,
				"namespace", req.Request.Namespace,
				"name", req.Request.Name,
			)
			log.Info("Reconciling ConfigMap")

			// Retrieve the provider cluster
			cl, err := provider.Get(ctx, req.ClusterName)
			if err != nil {
				return reconcile.Result{}, err
			}

			// Retrieve the ConfigMap that triggered the reconciler from the provider cluster
			cm := &corev1.ConfigMap{}
			if err := cl.GetClient().Get(ctx, req.Request.NamespacedName, cm); err != nil {
				if apierrors.IsNotFound(err) {
					return reconcile.Result{}, nil
				}
				return reconcile.Result{}, err
			}
			log.Info("ConfigMap retrieved")

			localCM := &corev1.ConfigMap{}
			err = localCl.GetClient().Get(ctx, req.Request.NamespacedName, localCM)
			if err != nil && !apierrors.IsNotFound(err) {
				return reconcile.Result{}, err
			}

			if apierrors.IsNotFound(err) {
				if err := cl.GetClient().Delete(ctx, cm); err != nil {
					return reconcile.Result{}, err
				}
				log.Info("No matching ConfigMap in local cluster, deleted from provider cluster")
				return reconcile.Result{}, nil
			}

			if cmp.Equal(localCM.Data, cm.Data) {
				log.Info("ConfigMap matches configmap in local cluster, nothing to do")
				return ctrl.Result{}, nil
			}

			cm.Data = localCM.Data
			if err := cl.GetClient().Update(ctx, cm); err != nil {
				return reconcile.Result{}, err
			}
			log.Info("ConfigMap updated to match local cluster")

			return ctrl.Result{}, nil
		},
	)
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
