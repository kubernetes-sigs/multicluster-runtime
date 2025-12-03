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
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/go-cmp/cmp"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// Clusters implements the common patterns around managing clusters
// observed in providers.
// It partially implements the multicluster.Provider interface.
type Clusters[T cluster.Cluster] struct {
	// ErrorHandler is called when an error occurs that cannot be
	// returned to a caller, e.g. when a cluster's Start method returns
	// an error.
	ErrorHandler func(error, string, ...any)

	// EqualClusters is used to compare two clusters for equality when
	// adding or replacing clusters.
	EqualClusters func(a, b T) bool

	// WaitCacheTimeout is the duration to wait for a cluster's cache
	// to sync when adding a new cluster.
	// Default is 30 seconds.
	WaitCacheTimeout time.Duration

	lock     sync.RWMutex
	clusters map[string]T
	cancels  map[string]context.CancelFunc
	// Indexers holds representations of all indexes that were applied
	// and should be applied to clusters that are added.
	indexers []Index
}

// Index represents an index on a field in a cluster.
type Index struct {
	Object    client.Object
	Field     string
	Extractor client.IndexerFunc
}

// New returns a new instance of Clusters.
func New[T cluster.Cluster]() Clusters[T] {
	return Clusters[T]{
		EqualClusters:    EqualClusters[T],
		WaitCacheTimeout: 30 * time.Second,
		clusters:         make(map[string]T),
		cancels:          make(map[string]context.CancelFunc),
		indexers:         []Index{},
	}
}

// ClusterNames returns the names of all clusters in a sorted order.
func (c *Clusters[T]) ClusterNames() []string {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return slices.Sorted(maps.Keys(c.clusters))
}

// Get returns the cluster with the given name as a cluster.Cluster.
// It implements the Get method from the Provider interface.
func (c *Clusters[T]) Get(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	return c.GetTyped(ctx, clusterName)
}

// GetTyped returns the cluster with the given name.
func (c *Clusters[T]) GetTyped(_ context.Context, clusterName string) (T, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	cl, ok := c.clusters[clusterName]
	if !ok {
		return *new(T), fmt.Errorf("cluster with name %s not found: %w", clusterName, multicluster.ErrClusterNotFound)
	}

	return cl, nil
}

// Add adds a new cluster.
// If a cluster with the given name already exists, it returns an error.
func (c *Clusters[T]) Add(ctx context.Context, clusterName string, cl T, aware multicluster.Aware) error {
	ctx, err := c.add(ctx, clusterName, cl)
	if err != nil {
		return err
	}

	go func() {
		defer c.Remove(clusterName)
		if err := cl.Start(ctx); err != nil {
			if c.ErrorHandler != nil {
				c.ErrorHandler(err, "error in cluster", "name", clusterName)
			}
		}
	}()

	waitCacheCtx, cancel := context.WithTimeout(ctx, c.WaitCacheTimeout)
	defer cancel()

	if !cl.GetCache().WaitForCacheSync(waitCacheCtx) {
		defer c.Remove(clusterName)
		return fmt.Errorf("timed out after %q waiting for cache to sync for cluster %s", c.WaitCacheTimeout, clusterName)
	}

	if aware != nil {
		if err := aware.Engage(ctx, clusterName, cl); err != nil {
			defer c.Remove(clusterName)
			return err
		}
	}

	c.lock.RLock()
	for _, index := range c.indexers {
		if err := cl.GetFieldIndexer().IndexField(ctx, index.Object, index.Field, index.Extractor); err != nil {
			c.lock.RUnlock()
			defer c.Remove(clusterName)
			return fmt.Errorf("failed to index field %s on cluster %s: %w", index.Field, clusterName, err)
		}
	}
	c.lock.RUnlock()

	return nil
}

func (c *Clusters[T]) add(ctx context.Context, clusterName string, cl T) (context.Context, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if _, exists := c.clusters[clusterName]; exists {
		return nil, fmt.Errorf("cluster with name %s already exists", clusterName)
	}

	ctx, cancel := context.WithCancel(ctx)
	c.clusters[clusterName] = cl
	c.cancels[clusterName] = cancel
	return ctx, nil
}

// Remove removes a cluster by name.
func (c *Clusters[T]) Remove(clusterName string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	if cancel, ok := c.cancels[clusterName]; ok {
		cancel()
	}
	delete(c.cancels, clusterName)
	delete(c.clusters, clusterName)
}

// EqualClusters compares two clusters for equality based on their
// configuration. It is the default implementation used by
// Clusters.AddOrReplace.
func EqualClusters[T cluster.Cluster](a, b T) bool {
	return cmp.Equal(a.GetConfig(), b.GetConfig())
}

// AddOrReplace adds or replaces a cluster with the given name.
// If a cluster with the name already exists it compares the
// configuration as returned by cluster.GetConfig() to compare
// clusters.
func (c *Clusters[T]) AddOrReplace(ctx context.Context, clusterName string, cl T, aware multicluster.Aware) error {
	existing, err := c.GetTyped(ctx, clusterName)
	if err != nil {
		// Cluster does not exist, add it
		return c.Add(ctx, clusterName, cl, aware)
	}

	if c.EqualClusters(existing, cl) {
		// Cluster already exists with the same config, nothing to do
		return nil
	}

	// Cluster exists with a different config, replace it
	c.Remove(clusterName)
	return c.Add(ctx, clusterName, cl, aware)
}

// IndexField indexes a field on all clusters.
// It implements the IndexField method from the Provider interface.
// Clusters engaged after this call will also have the index applied.
func (c *Clusters[T]) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	c.lock.Lock()
	c.indexers = append(c.indexers, Index{
		Object:    obj,
		Field:     field,
		Extractor: extractValue,
	})
	clusters := maps.Clone(c.clusters)
	c.lock.Unlock()

	// The standard cache implementation errors if the same object/field
	// is indexed multiple times. But custom cache implementations may
	// not error, so there's no tracking happening, just register the
	// indexes in and apply them to all clusters.

	var errs error
	for name, cl := range clusters {
		if err := cl.GetFieldIndexer().IndexField(ctx, obj, field, extractValue); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to index field on cluster %q: %w", name, err))
		}
	}
	return errs
}

// Indexers returns all indexers that have been applied to clusters.
func (c *Clusters[T]) Indexers() []Index {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return slices.Clone(c.indexers)
}
