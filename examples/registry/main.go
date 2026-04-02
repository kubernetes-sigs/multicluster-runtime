/*
Copyright 2026 The Kubernetes Authors.

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
	"log"
	"time"

	"golang.org/x/sync/errgroup"

	"k8s.io/klog/v2"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	"sigs.k8s.io/multicluster-runtime/pkg/clusters"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/providers/namespace"
)

func init() {
	ctrl.SetLogger(klog.Background())
}

func main() {
	ctx := signals.SetupSignalHandler()

	if err := run(ctx); err != nil {
		log.Fatalf("failed to run controller: %v", err)
	}
}

func run(ctx context.Context) error {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	cl, err := cluster.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create cluster: %w", err)
	}
	provider := namespace.New(cl)

	mgr, err := mcmanager.New(cfg, provider, mcmanager.Options{})
	if err != nil {
		return fmt.Errorf("unable to set up overall controller manager: %w", err)
	}
	registry := clusters.NewRegistry[cluster.Cluster]()
	if err := mgr.Add(registry); err != nil {
		return fmt.Errorf("error adding registry as runnable to manager: %w", err)
	}

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return ignoreCanceled(cl.Start(ctx))
	})
	g.Go(func() error {
		return ignoreCanceled(mgr.Start(ctx))
	})
	g.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return nil
			default:
				fmt.Println("current clusters:")
				for _, clusterName := range registry.ClusterNames() {
					fmt.Printf("  - %s\n", clusterName)
				}
				time.Sleep(1 * time.Second)
			}
		}
	})
	return g.Wait()
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
