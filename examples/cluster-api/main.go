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
	"fmt"
	"os"

	capiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	capi "sigs.k8s.io/multicluster-runtime/providers/cluster-api"
)

func init() {
	runtime.Must(capiv1beta1.AddToScheme(scheme.Scheme))
}

func main() {
	ctrllog.SetLogger(zap.New(zap.UseDevMode(true)))
	entryLog := ctrllog.Log.WithName("entrypoint")
	ctx := signals.SetupSignalHandler()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		entryLog.Error(err, "unable to get kubeconfig")
		os.Exit(1)
	}

	// Create the Cluster API provider.
	provider := capi.New(capi.Options{})

	// Create a multi-cluster manager with the provider.
	entryLog.Info("Setting up multi-cluster manager")
	mcMgr, err := mcmanager.New(cfg, provider, mcmanager.Options{
		Client: client.Options{
			Cache: &client.CacheOptions{
				Unstructured: true,
				DisableFor:   []client.Object{&corev1.Secret{}},
			},
		},
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})
	if err != nil {
		entryLog.Error(err, "unable to set up multi-cluster manager")
		os.Exit(1)
	}

	// Set up the provider controller on the manager.
	if err := provider.SetupWithManager(mcMgr); err != nil {
		entryLog.Error(err, "unable to set up provider")
		os.Exit(1)
	}

	// Create a configmap controller in the multi-cluster manager.
	if err := mcbuilder.ControllerManagedBy(mcMgr).
		Named("multicluster-configmaps").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(
			func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
				log := ctrllog.FromContext(ctx).WithValues("cluster", req.ClusterName)
				log.Info("Reconciling ConfigMap")

				cl, err := mcMgr.GetCluster(ctx, req.ClusterName)
				if err != nil {
					return reconcile.Result{}, err
				}

				cm := &corev1.ConfigMap{}
				if err := cl.GetClient().Get(ctx, req.Request.NamespacedName, cm); err != nil {
					if apierrors.IsNotFound(err) {
						return reconcile.Result{}, nil
					}
					return reconcile.Result{}, err
				}

				log.Info(fmt.Sprintf("ConfigMap %s/%s in cluster %q", cm.Namespace, cm.Name, req.ClusterName))

				return ctrl.Result{}, nil
			},
		)); err != nil {
		entryLog.Error(err, "failed to build controller")
		os.Exit(1)
	}

	// Start the manager.
	if err := ignoreCanceled(mcMgr.Start(ctx)); err != nil {
		entryLog.Error(err, "unable to start manager")
		os.Exit(1)
	}
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
