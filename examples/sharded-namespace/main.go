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

// examples/sharded-namespace/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sync/errgroup"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/manager/coordinator/sharded"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/providers/namespace"
)

func main() {
	klog.Background() // ensure klog initialized
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	log := ctrl.Log.WithName("sharded-example")

	ctx := ctrl.SetupSignalHandler()

	if err := run(ctx); err != nil {
		log.Error(err, "exiting")
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	// use in-cluster config; fall back to default loading rules for local runs
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("get kubeconfig: %w", err)
	}

	// Provider: treats namespaces in the host cluster as “downstream clusters”.
	host, err := cluster.New(cfg)
	if err != nil {
		return fmt.Errorf("create host cluster: %w", err)
	}
	provider := namespace.New(host)

	// Direct client for coordinator (uses host scheme).
	kubeClient, err := client.New(cfg, client.Options{Scheme: host.GetScheme()})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	// Multicluster manager (no peer ID passed; pod hostname becomes peer ID).
	// Configure sharding:
	// - fencing prefix: "mcr-shard" (per-cluster Lease names become mcr-shard-<cluster>)
	// - peer membership still uses "mcr-peer" internally (set in WithMultiCluster)
	// Peer ID defaults to os.Hostname().
	mgr, err := mcmanager.New(cfg, provider, manager.Options{},
		mcmanager.WithCoordinator(
			sharded.New(kubeClient, ctrl.Log.WithName("sharded-coordinator"),
				sharded.WithShardLease("kube-system", "mcr-shard"),
				sharded.WithPerClusterLease(true),
			),
		),
	)
	if err != nil {
		return fmt.Errorf("create mc manager: %w", err)
	}

	// A simple controller that logs ConfigMaps per owned “cluster” (namespace).
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("multicluster-configmaps").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			// attach cluster once; don't repeat it in Info()
			l := ctrl.LoggerFrom(ctx).WithValues("cluster", req.ClusterName)

			// get the right cluster client
			cl, err := mgr.GetCluster(ctx, req.ClusterName)
			if err != nil {
				return ctrl.Result{}, err
			}

			// fetch the object, then log from the object (truth)
			cm := &corev1.ConfigMap{}
			if err := cl.GetClient().Get(ctx, req.Request.NamespacedName, cm); err != nil {
				if apierrors.IsNotFound(err) {
					// object vanished — nothing to do
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			}

			// now cm.Namespace is accurate (e.g., "zoo", "kube-system", etc.)
			l.Info("Reconciling ConfigMap",
				"ns", cm.GetNamespace(),
				"name", cm.GetName(),
			)

			// show which peer handled it (pod hostname)
			if host, _ := os.Hostname(); host != "" {
				l.Info("Handled by peer", "peer", host)
			}
			return ctrl.Result{}, nil
		})); err != nil {
		return fmt.Errorf("build controller: %w", err)
	}

	// Start everything
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return ignoreCanceled(host.Start(ctx)) })
	g.Go(func() error { return ignoreCanceled(mgr.Start(ctx)) })
	return g.Wait()
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
