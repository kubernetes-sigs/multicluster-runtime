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

package reconcile

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// ClusterNotFoundWrapper wraps an existing [reconcile.TypedReconciler] and ignores [multicluster.ErrClusterNotFound] errors.
type ClusterNotFoundWrapper[request comparable] struct {
	wrapped reconcile.TypedReconciler[request]
}

// NewClusterNotFoundWrapper creates a new [ClusterNotFoundWrapper].
func NewClusterNotFoundWrapper[request comparable](w reconcile.TypedReconciler[request]) reconcile.TypedReconciler[request] {
	return &ClusterNotFoundWrapper[request]{wrapped: w}
}

// Reconcile implements [reconcile.TypedReconciler].
func (r *ClusterNotFoundWrapper[request]) Reconcile(ctx context.Context, req request) (reconcile.Result, error) {
	res, err := r.wrapped.Reconcile(ctx, req)

	// if the error returned by the reconciler is ErrClusterNotFound, we return without requeuing.
	if errors.Is(err, multicluster.ErrClusterNotFound) {
		return reconcile.Result{}, nil
	}

	return res, err
}

// String returns a string representation of the wrapped reconciler.
func (r *ClusterNotFoundWrapper[request]) String() string {
	return fmt.Sprintf("%v", r.wrapped)
}

// clusterOwner is the subset of Manager needed to check cluster ownership.
// Declared locally (rather than depending on the manager package directly)
// to avoid an import cycle: pkg/manager already depends on pkg/reconcile.
// Any Manager satisfies this structurally.
type clusterOwner interface {
	Owns(clusterName multicluster.ClusterName) bool
}

// localCluster mirrors manager.LocalCluster: the empty cluster name
// identifies the local (host) cluster, which is never subject to ownership
// gating.
const localCluster = multicluster.ClusterName("")

// OwnershipWrapper wraps an existing [reconcile.TypedReconciler] and skips
// invoking it for requests whose cluster this process does not currently
// own, per the Manager's configured Coordinator.
//
// A source that is not scoped to a coordinator-engaged cluster, for example
// one registered against a fixed, shared cluster via Source.ForCluster, can
// enqueue requests naming any cluster the provider knows. Without this
// wrapper they would be reconciled on every process. It also re-checks
// ownership on every dequeue,
// so a request that was queued (or requeued) while this process owned a
// cluster, but is processed after ownership moved to another peer, is
// skipped rather than acted on.
type OwnershipWrapper[request ClusterAware[request]] struct {
	wrapped reconcile.TypedReconciler[request]
	mgr     clusterOwner
}

// NewOwnershipWrapper creates a new [OwnershipWrapper].
func NewOwnershipWrapper[request ClusterAware[request]](w reconcile.TypedReconciler[request], mgr clusterOwner) reconcile.TypedReconciler[request] {
	return &OwnershipWrapper[request]{wrapped: w, mgr: mgr}
}

// Reconcile implements [reconcile.TypedReconciler].
func (r *OwnershipWrapper[request]) Reconcile(ctx context.Context, req request) (reconcile.Result, error) {
	if name := req.Cluster(); name != localCluster {
		if !r.mgr.Owns(name) {
			return reconcile.Result{}, nil
		}
	}

	return r.wrapped.Reconcile(ctx, req)
}

// String returns a string representation of the wrapped reconciler.
func (r *OwnershipWrapper[request]) String() string {
	return fmt.Sprintf("%v", r.wrapped)
}
