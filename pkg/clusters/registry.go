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

package clusters

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// Ensure Registry implements the Runnable and Aware interfaces so it
// can be added to a multicluster manager and will be notified of
// cluster changes.
var _ manager.Runnable = (*Registry[cluster.Cluster])(nil)
var _ multicluster.Aware = (*Registry[cluster.Cluster])(nil)

// Registry is a simple implementation of a cluster registry that can be
// added to a multicluster manager as a runnable.
type Registry[T cluster.Cluster] struct {
	// EqualClusters is used to compare two clusters for equality when
	// adding or replacing clusters.
	EqualClusters func(a, b T) bool

	lock     sync.RWMutex
	clusters map[multicluster.ClusterName]T
}

// NewRegistry creates a new Registry.
func NewRegistry[T cluster.Cluster]() *Registry[T] {
	return &Registry[T]{
		EqualClusters: EqualClusters[T],
		clusters:      make(map[multicluster.ClusterName]T),
	}
}

// Start is a no-op since the registry does not have any background
// operations, but it is required to implement the Runnable interface.
func (r *Registry[T]) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Engage adds or replaces the cluster with the given name in the registry.
func (r *Registry[T]) Engage(ctx context.Context, name multicluster.ClusterName, cl cluster.Cluster) error {
	if err := r.AddOrReplace(ctx, name, cl.(T)); err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		r.RemoveIfEqual(name, cl.(T))
	}()
	return nil
}

// ClusterNames returns the names of all clusters in a sorted order.
func (r *Registry[T]) ClusterNames() []multicluster.ClusterName {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return slices.Sorted(maps.Keys(r.clusters))
}

// Get returns the cluster with the given name as a cluster.Cluster.
// It implements the Get method from the Provider interface.
func (r *Registry[T]) Get(ctx context.Context, name multicluster.ClusterName) (cluster.Cluster, error) {
	return r.GetTyped(ctx, name)
}

// GetTyped returns the cluster with the given name.
func (r *Registry[T]) GetTyped(_ context.Context, name multicluster.ClusterName) (T, error) {
	r.lock.RLock()
	defer r.lock.RUnlock()

	cl, ok := r.clusters[name]
	if !ok {
		return *new(T), fmt.Errorf("cluster with name %s not found: %w", name, multicluster.ErrClusterNotFound)
	}

	return cl, nil
}

// Add adds a new cluster.
// If a cluster with the given name already exists, it returns an error.
func (r *Registry[T]) Add(ctx context.Context, name multicluster.ClusterName, cl T) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	if _, exists := r.clusters[name]; exists {
		return fmt.Errorf("cluster with name %s already exists", name)
	}

	r.clusters[name] = cl
	return nil
}

// Remove removes a cluster by name.
func (r *Registry[T]) Remove(name multicluster.ClusterName) {
	r.lock.Lock()
	defer r.lock.Unlock()
	delete(r.clusters, name)
}

// RemoveIfEqual removes a cluster if the currently stored cluster is
// equal to the passed cluster.
func (r *Registry[T]) RemoveIfEqual(name multicluster.ClusterName, cl T) {
	r.lock.Lock()
	defer r.lock.Unlock()
	stored, ok := r.clusters[name]
	if !ok {
		return
	}
	if any(cl) != any(stored) {
		return
	}
	delete(r.clusters, name)
}

// AddOrReplace adds or replaces a cluster with the given name.
// If a cluster with the name already exists it compares the
// configuration as returned by cluster.GetConfig() to compare
// clusters.
func (r *Registry[T]) AddOrReplace(ctx context.Context, name multicluster.ClusterName, cl T) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	if _, exists := r.clusters[name]; exists {
		if r.EqualClusters(r.clusters[name], cl) {
			// Cluster already exists with the same config, nothing to do
			return nil
		}
		// Cluster exists with a different config, replace it
		delete(r.clusters, name)
	}

	// Cluster does not exist or was removed
	r.clusters[name] = cl
	return nil
}

// ForEach iterates over all clusters in the registry and calls the function fn with each cluster and its name.
// If the function returns an error ForEach will stop and return that error.
// ForEach will lock the registry for the duration of the execution.
func (r *Registry[T]) ForEach(fn func(name multicluster.ClusterName, cl T) error) error {
	r.lock.RLock()
	defer r.lock.RUnlock()

	for name, cl := range r.clusters {
		if err := fn(name, cl); err != nil {
			return err
		}
	}

	return nil
}
