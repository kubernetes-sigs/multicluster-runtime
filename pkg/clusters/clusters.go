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
type Clusters[O cluster.Cluster] struct {
	Lock     sync.RWMutex
	Clusters map[string]O
	Cancels  map[string]context.CancelFunc
}

// New returns a new instance of Clusters.
func New[O cluster.Cluster]() Clusters[O] {
	return Clusters[O]{
		Clusters: make(map[string]O),
		Cancels:  make(map[string]context.CancelFunc),
	}
}

// ClusterNames returns the names of all clusters in a sorted order.
func (c *Clusters[O]) ClusterNames() []string {
	c.Lock.RLock()
	defer c.Lock.RUnlock()
	return slices.Sorted(maps.Keys(c.Clusters))
}

// Get returns the cluster with the given name as a cluster.Cluster.
// It implements the Get method from the Provider interface.
func (c *Clusters[O]) Get(ctx context.Context, name string) (cluster.Cluster, error) {
	return c.GetTyped(ctx, name)
}

// GetTyped returns the cluster with the given name.
func (c *Clusters[O]) GetTyped(_ context.Context, name string) (O, error) {
	c.Lock.RLock()
	defer c.Lock.RUnlock()

	cl, ok := c.Clusters[name]
	if !ok {
		return *new(O), fmt.Errorf("cluster with name %s not found", name)
	}

	return cl, nil
}

// HandleClusterErrorFunc is called when a cluster encounters an error
// during its lifecycle.
type HandleClusterErrorFunc func(string, error)

// Add adds a new cluster.
// If a cluster with the given name already exists, it returns an error.
func (c *Clusters[O]) Add(ctx context.Context, name string, cl O, aware multicluster.Aware, handleError HandleClusterErrorFunc) error {
	c.Lock.Lock()
	defer c.Lock.Unlock()

	if _, exists := c.Clusters[name]; exists {
		return fmt.Errorf("cluster with name %s already exists", name)
	}

	ctx, cancel := context.WithCancel(ctx)
	if err := aware.Engage(ctx, name, cl); err != nil {
		cancel()
		return err
	}

	c.Clusters[name] = cl
	c.Cancels[name] = cancel
	go func() {
		defer c.Remove(name)
		if err := cl.Start(ctx); err != nil {
			handleError(name, err)
		}
	}()

	return nil
}

// Remove removes a cluster by name.
func (c *Clusters[O]) Remove(name string) {
	c.Lock.Lock()
	defer c.Lock.Unlock()

	if cancel, ok := c.Cancels[name]; ok {
		cancel()
		delete(c.Clusters, name)
		delete(c.Cancels, name)
	}
}

// AddOrReplace adds or replaces a cluster with the given name.
// If a cluster with the name already exists it compares the
// configuration as returned by cluster.GetConfig() to compare
// clusters.
func (c *Clusters[O]) AddOrReplace(ctx context.Context, name string, cl O, aware multicluster.Aware, handleError HandleClusterErrorFunc) error {
	existing, err := c.Get(ctx, name)
	if err != nil {
		// Cluster does not exist, add it
		return c.Add(ctx, name, cl, aware, handleError)
	}

	if cmp.Equal(existing.GetConfig(), cl.GetConfig()) {
		// Cluster already exists with the same config, nothing to do
		return nil
	}

	// Cluster exists with a different config, replace it
	c.Remove(name)
	return c.Add(ctx, name, cl, aware, handleError)
}

// IndexField indexes a field on all clusters.
// It implements the IndexField method from the Provider interface.
func (c *Clusters[O]) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	c.Lock.RLock()
	defer c.Lock.RUnlock()

	var errs error
	for name, cl := range c.Clusters {
		if err := cl.GetCache().IndexField(ctx, obj, field, extractValue); err != nil {
			errs = errors.Join(errs, fmt.Errorf("failed to index field on cluster %q: %w", name, err))
		}
	}
	return errs
}
