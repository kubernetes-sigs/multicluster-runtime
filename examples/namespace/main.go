/*
Copyright 2024 The Kubernetes Authors.

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

	"github.com/go-logr/logr"
	flag "github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/providers/namespace"
)

func init() {
	ctrl.SetLogger(klog.Background())
}

func main() {
	ctrllog.SetLogger(zap.New(zap.UseDevMode(true)))
	entryLog := ctrllog.Log.WithName("entrypoint")
	ctx := signals.SetupSignalHandler()

	kubeconfig := flag.String("kubeconfig", "", "path to the kubeconfig file. If not given a test env is started.")
	flag.Parse()

	if err := run(ctx, entryLog, ptr.Deref(kubeconfig, "")); err != nil {
		entryLog.Error(err, "failed to run controller")
		os.Exit(1)
	}
}

func run(ctx context.Context, log logr.Logger, kubeconfig string) error {
	var cfg *rest.Config
	if kubeconfig == "" {
		testEnv := &envtest.Environment{}
		var err error
		cfg, err = testEnv.Start()
		if err != nil {
			return fmt.Errorf("failed to start local environment: %w", err)
		}
		defer func() {
			if testEnv == nil {
				return
			}
			if err := testEnv.Stop(); err != nil {
				log.Error(err, "failed to stop local environment")
				os.Exit(1)
			}
		}()
	} else {
		var err error
		cfg, err = ctrl.GetConfig()
		if err != nil {
			return fmt.Errorf("failed to get kubeconfig: %w", err)
		}
	}

	// Test fixtures
	cli, err := client.New(cfg, client.Options{})
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	log.Info("Creating Namespace and ConfigMap objects")
	runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "zoo"}})))
	runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "elephant"}})))
	runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "lion"}})))
	runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "jungle"}})))
	runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "monkey"}})))
	runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "island"}})))
	runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "bird"}})))

	cl, err := cluster.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create cluster: %w", err)
	}
	provider := namespace.New(cl)

	// Setup a cluster-aware Manager, with the provider to lookup clusters.
	log.Info("Setting up cluster-aware manager")
	mgr, err := mcmanager.New(cfg, provider, mcmanager.Options{})
	if err != nil {
		return fmt.Errorf("unable to set up overall controller manager: %w", err)
	}

	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("multicluster-configmaps").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(
			func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
				log := ctrllog.FromContext(ctx).WithValues("cluster", req.ClusterName)
				log.Info("Reconciling ConfigMap")

				cl, err := mgr.GetCluster(ctx, req.ClusterName)
				if err != nil {
					return reconcile.Result{}, err
				}

				cm := &corev1.ConfigMap{}
				if err := cl.GetClient().Get(ctx, req.NamespacedName, cm); err != nil {
					if apierrors.IsNotFound(err) {
						return reconcile.Result{}, nil
					}
					return reconcile.Result{}, err
				}

				log.Info("ConfigMap %s/%s in cluster %q", cm.Namespace, cm.Name, req.ClusterName)

				return ctrl.Result{}, nil
			},
		)); err != nil {
		return fmt.Errorf("unable to build controller: %w", err)
	}

	// Starting everything.
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return ignoreCanceled(provider.Run(ctx, mgr))
	})
	g.Go(func() error {
		return ignoreCanceled(cl.Start(ctx))
	})
	g.Go(func() error {
		return ignoreCanceled(mgr.Start(ctx))
	})
	if err := g.Wait(); err != nil {
		return fmt.Errorf("unable to start: %w", err)
	}

	return nil
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
