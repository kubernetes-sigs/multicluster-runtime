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

// Package context provides utilities for working with cluster context.
//
//nolint:revive // Package name intentionally shadows standard library for domain clarity
package context

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

type clusterKeyType string

const clusterKey clusterKeyType = "cluster"

// WithCluster returns a new context with the given cluster.
func WithCluster(ctx context.Context, cluster string) context.Context {
	return context.WithValue(ctx, clusterKey, cluster)
}

// ClusterFrom returns the cluster from the context.
func ClusterFrom(ctx context.Context) (string, bool) {
	cluster, ok := ctx.Value(clusterKey).(string)
	return cluster, ok
}

// ReconcilerWithClusterInContext returns a reconciler that sets the cluster name in the
// context.
func ReconcilerWithClusterInContext(r reconcile.Reconciler) mcreconcile.Reconciler {
	return reconcile.TypedFunc[mcreconcile.Request](func(ctx context.Context, req mcreconcile.Request) (reconcile.Result, error) {
		ctx = WithCluster(ctx, req.ClusterName)
		return r.Reconcile(ctx, req.Request)
	})
}
