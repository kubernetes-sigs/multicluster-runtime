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

	Lock     sync.RWMutex
	Clusters map[string]T
	Cancels  map[string]context.CancelFunc
	// Indexers holds representations of all indexes that were applied
	// and should be applied to clusters that are added.
	Indexers []Index
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
		Clusters: make(map[string]T),
		Cancels:  make(map[string]context.CancelFunc),
		Indexers: []Index{},
	}
}

// ClusterNames returns the names of all clusters in a sorted order.
func (c *Clusters[T]) ClusterNames() []string {
	c.Lock.RLock()
	defer c.Lock.RUnlock()
	return slices.Sorted(maps.Keys(c.Clusters))
}

// Get returns the cluster with the given name as a cluster.Cluster.
// It implements the Get method from the Provider interface.
func (c *Clusters[T]) Get(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	return c.GetTyped(ctx, clusterName)
}

// GetTyped returns the cluster with the given name.
func (c *Clusters[T]) GetTyped(_ context.Context, clusterName string) (T, error) {
	c.Lock.RLock()
	defer c.Lock.RUnlock()

	cl, ok := c.Clusters[clusterName]
	if !ok {
		return *new(T), fmt.Errorf("cluster with name %s not found: %w", clusterName, multicluster.ErrClusterNotFound)
	}

	return cl, nil
}

// Add adds a new cluster.
// If a cluster with the given name already exists, it returns an error.
func (c *Clusters[T]) Add(ctx context.Context, clusterName string, cl T, aware multicluster.Aware) error {
	c.Lock.Lock()
	defer c.Lock.Unlock()

	if _, exists := c.Clusters[clusterName]; exists {
		return fmt.Errorf("cluster with name %s already exists", clusterName)
	}

	ctx, cancel := context.WithCancel(ctx)
	if aware != nil {
		if err := aware.Engage(ctx, clusterName, cl); err != nil {
			cancel()
			return err
		}
	}

	c.Clusters[clusterName] = cl
	c.Cancels[clusterName] = cancel
	go func() {
		defer c.Remove(clusterName)
		if err := cl.Start(ctx); err != nil {
			if c.ErrorHandler != nil {
				c.ErrorHandler(err, "error in cluster", "name", clusterName)
			}
		}
	}()

	for _, index := range c.Indexers {
		if err := cl.GetFieldIndexer().IndexField(ctx, index.Object, index.Field, index.Extractor); err != nil {
			defer c.Remove(clusterName)
			return fmt.Errorf("failed to index field %s on cluster %s: %w", index.Field, clusterName, err)
		}
	}

	return nil
}

// Remove removes a cluster by name.
func (c *Clusters[T]) Remove(clusterName string) {
	c.Lock.Lock()
	defer c.Lock.Unlock()

	if cancel, ok := c.Cancels[clusterName]; ok {
		cancel()
	}
	delete(c.Cancels, clusterName)
	delete(c.Clusters, clusterName)
}

// AddOrReplace adds or replaces a cluster with the given name.
// If a cluster with the name already exists it compares the
// configuration as returned by cluster.GetConfig() to compare
// clusters.
func (c *Clusters[T]) AddOrReplace(ctx context.Context, clusterName string, cl T, aware multicluster.Aware) error {
	existing, err := c.Get(ctx, clusterName)
	if err != nil {
		// Cluster does not exist, add it
		return c.Add(ctx, clusterName, cl, aware)
	}

	if cmp.Equal(existing.GetConfig(), cl.GetConfig()) {
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
	c.Lock.Lock()
	c.Indexers = append(c.Indexers, Index{
		Object:    obj,
		Field:     field,
		Extractor: extractValue,
	})
	c.Lock.Unlock()

	var errs error
	c.Lock.RLock()
	for name, cl := range c.Clusters {
		if err := cl.GetFieldIndexer().IndexField(ctx, obj, field, extractValue); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to index field on cluster %q: %w", name, err))
		}
	}
	c.Lock.RUnlock()
	return errs
}
