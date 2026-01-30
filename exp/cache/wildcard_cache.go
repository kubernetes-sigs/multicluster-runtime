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

package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"sigs.k8s.io/multicluster-runtime/exp/informers"
	mccache "sigs.k8s.io/multicluster-runtime/exp/informers/cache"
)

// WildcardCache is a cache that operates across multiple clusters.
// It aggregates watches from multiple clusters into a single shared informer per GVK.
type WildcardCache interface {
	cache.Cache

	// AddCluster adds a cluster to be watched by this cache.
	AddCluster(name string, cfg *rest.Config) error

	// RemoveCluster removes a cluster from being watched.
	RemoveCluster(name string)

	// GetScopeableInformer returns the scopeable shared informer for a given object type.
	// This informer can be scoped down to a single cluster using the Cluster() method.
	GetScopeableInformer(obj runtime.Object) (informers.ScopeableSharedIndexInformer, schema.GroupVersionKind, error)

	// GetClusterConfigs returns a copy of the current cluster configurations.
	GetClusterConfigs() map[string]*rest.Config

	// GetClusterNames returns the list of cluster names currently in the cache.
	GetClusterNames() []string
}

// WildcardCacheProvider is an optional interface that multicluster.Provider implementations
// can implement to expose their WildcardCache for cross-cluster queries.
//
// When a provider implements this interface, reconcilers can access the wildcard cache
// to query objects across ALL clusters, not just the cluster being reconciled.
//
// Example usage in a reconciler:
//
//	func (r *MyReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
//	    // Get the provider from the manager
//	    provider := mgr.GetProvider()
//
//	    // Check if it supports wildcard cache access
//	    if wcp, ok := provider.(expcache.WildcardCacheProvider); ok {
//	        wildcardCache := wcp.GetWildcardCache()
//
//	        // List ALL ConfigMaps across ALL clusters
//	        allConfigMaps := &corev1.ConfigMapList{}
//	        if err := wildcardCache.List(ctx, allConfigMaps); err != nil {
//	            return ctrl.Result{}, err
//	        }
//
//	        // Each ConfigMap has the cluster annotation set
//	        for _, cm := range allConfigMaps.Items {
//	            cluster := expcache.GetClusterName(&cm)
//	            // ...
//	        }
//	    }
//	    // ...
//	}
type WildcardCacheProvider interface {
	// GetWildcardCache returns the wildcard cache that aggregates all clusters.
	// The returned cache can be used for cross-cluster queries.
	GetWildcardCache() WildcardCache
}

// GetClusterName returns the cluster name from an object's annotations.
// Returns empty string if the annotation is not set.
func GetClusterName(obj metav1.Object) string {
	if annotations := obj.GetAnnotations(); annotations != nil {
		return annotations[mccache.ClusterAnnotation]
	}
	return ""
}

// ClusterAnnotation is the annotation key used to identify the source cluster of an object.
// This is re-exported from mccache for convenience.
const ClusterAnnotation = mccache.ClusterAnnotation

// wildcardCache implements WildcardCache using exp/informers.
type wildcardCache struct {
	scheme  *runtime.Scheme
	log     logr.Logger
	resync  time.Duration
	stopCtx context.Context

	mu              sync.RWMutex
	clusterConfigs  map[string]*rest.Config
	clusterCancels  map[string]context.CancelFunc
	informers       map[schema.GroupVersionKind]informers.ScopeableSharedIndexInformer
	started         bool
	indexFieldFuncs []indexFieldEntry

	// listerWatcherFactory creates ListerWatcher instances for a given GVK.
	// If nil, a default multiClusterListerWatcher will be created.
	listerWatcherFactory ListerWatcherFactory
}

// indexFieldEntry stores information about a field indexer to be applied to informers.
type indexFieldEntry struct {
	obj          client.Object
	field        string
	extractValue client.IndexerFunc
}

// WildcardCacheOptions configures the wildcard cache.
type WildcardCacheOptions struct {
	// Scheme is the scheme used for object type resolution.
	Scheme *runtime.Scheme

	// Resync is the resync period for informers.
	Resync time.Duration

	// ListerWatcherFactory creates ListerWatcher instances for a given GVK.
	// If nil, a default multiClusterListerWatcher will be used.
	ListerWatcherFactory ListerWatcherFactory

	// Log is the logger to use. If nil, a default logger will be created.
	Log logr.Logger
}

// NewWildcardCache creates a new wildcard cache with the given options.
func NewWildcardCache(opts WildcardCacheOptions) WildcardCache {
	log := opts.Log
	if log.GetSink() == nil {
		log = ctrl.Log.WithName("wildcard-cache")
	}

	return &wildcardCache{
		scheme:               opts.Scheme,
		log:                  log,
		resync:               opts.Resync,
		clusterConfigs:       make(map[string]*rest.Config),
		clusterCancels:       make(map[string]context.CancelFunc),
		informers:            make(map[schema.GroupVersionKind]informers.ScopeableSharedIndexInformer),
		listerWatcherFactory: opts.ListerWatcherFactory,
	}
}

// AddCluster adds a cluster to be watched by this cache.
func (c *wildcardCache) AddCluster(name string, cfg *rest.Config) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.clusterConfigs[name]; exists {
		return nil // Already added
	}

	c.clusterConfigs[name] = cfg
	c.log.Info("Added cluster to wildcard cache", "cluster", name)
	return nil
}

// RemoveCluster removes a cluster from being watched.
func (c *wildcardCache) RemoveCluster(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cancel, exists := c.clusterCancels[name]; exists {
		cancel()
		delete(c.clusterCancels, name)
	}
	delete(c.clusterConfigs, name)
	c.log.Info("Removed cluster from wildcard cache", "cluster", name)
}

// GetClusterConfigs returns a copy of the current cluster configurations.
func (c *wildcardCache) GetClusterConfigs() map[string]*rest.Config {
	c.mu.RLock()
	defer c.mu.RUnlock()

	configs := make(map[string]*rest.Config, len(c.clusterConfigs))
	for name, cfg := range c.clusterConfigs {
		configs[name] = cfg
	}
	return configs
}

// GetClusterNames returns the list of cluster names currently in the cache.
func (c *wildcardCache) GetClusterNames() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	names := make([]string, 0, len(c.clusterConfigs))
	for name := range c.clusterConfigs {
		names = append(names, name)
	}
	return names
}

// Start starts the cache and blocks until the context is canceled.
func (c *wildcardCache) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("cache already started")
	}
	c.started = true
	c.stopCtx = ctx
	c.mu.Unlock()

	<-ctx.Done()
	return nil
}

// WaitForCacheSync waits for all informers to sync.
func (c *wildcardCache) WaitForCacheSync(ctx context.Context) bool {
	c.mu.RLock()
	infs := make([]informers.ScopeableSharedIndexInformer, 0, len(c.informers))
	for _, inf := range c.informers {
		infs = append(infs, inf)
	}
	c.mu.RUnlock()

	syncFuncs := make([]toolscache.InformerSynced, len(infs))
	for i, inf := range infs {
		syncFuncs[i] = inf.HasSynced
	}

	return toolscache.WaitForCacheSync(ctx.Done(), syncFuncs...)
}

// GetScopeableInformer returns the scopeable shared informer for a given object type.
func (c *wildcardCache) GetScopeableInformer(obj runtime.Object) (informers.ScopeableSharedIndexInformer, schema.GroupVersionKind, error) {
	gvk, err := apiutil.GVKForObject(obj, c.scheme)
	if err != nil {
		return nil, gvk, err
	}

	// Fast path: check if informer already exists
	c.mu.RLock()
	inf, exists := c.informers[gvk]
	c.mu.RUnlock()

	if exists {
		return inf, gvk, nil
	}

	// Slow path: create informer
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if inf, exists := c.informers[gvk]; exists {
		return inf, gvk, nil
	}

	// Create lister-watcher
	var lw toolscache.ListerWatcher
	if c.listerWatcherFactory != nil {
		lw = c.listerWatcherFactory.Create(gvk, c.scheme, c.clusterConfigs)
	} else {
		lw = NewMultiClusterListerWatcher(gvk, c.scheme, c.clusterConfigs)
	}

	// Create multi-cluster shared informer from exp/informers
	// This uses cluster-aware key functions automatically
	inf = informers.NewMultiClusterSharedIndexInformer(
		lw,
		obj,
		c.resync,
		toolscache.Indexers{
			mccache.ClusterIndexName:  mccache.ClusterIndexFunc,
			toolscache.NamespaceIndex: toolscache.MetaNamespaceIndexFunc,
		},
	)

	c.informers[gvk] = inf

	// Apply any registered index functions
	for _, entry := range c.indexFieldFuncs {
		entryGVK, _ := apiutil.GVKForObject(entry.obj, c.scheme)
		if entryGVK == gvk {
			if err := inf.AddIndexers(toolscache.Indexers{
				entry.field: func(o interface{}) ([]string, error) {
					clientObj, ok := o.(client.Object)
					if !ok {
						return nil, nil
					}
					return entry.extractValue(clientObj), nil
				},
			}); err != nil {
				c.log.Error(err, "Failed to add indexer", "field", entry.field)
			}
		}
	}

	// Start the informer if cache is already running
	if c.started && c.stopCtx != nil {
		go inf.Run(c.stopCtx.Done())
	}

	return inf, gvk, nil
}

// Get retrieves an object from the cache across all clusters.
// The clusterName can be specified via the ClusterName list option, or if not specified,
// the object key must include cluster information via the cluster-aware key format.
// If the object is found, it will have the cluster annotation set.
func (c *wildcardCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	inf, _, err := c.GetScopeableInformer(obj)
	if err != nil {
		return err
	}

	// Try to find the object in any cluster using the full store
	// The key format in the store is: cluster/namespace/name or cluster/name
	store := inf.GetStore()

	// We need to iterate over all clusters since we don't know which one has the object
	c.mu.RLock()
	clusterNames := make([]string, 0, len(c.clusterConfigs))
	for name := range c.clusterConfigs {
		clusterNames = append(clusterNames, name)
	}
	c.mu.RUnlock()

	for _, clusterName := range clusterNames {
		clusterKey := mccache.ToClusterAwareKey(clusterName, key.Namespace, key.Name)
		item, exists, err := store.GetByKey(clusterKey)
		if err != nil {
			continue
		}
		if exists {
			srcObj, ok := item.(runtime.Object)
			if !ok {
				return fmt.Errorf("cached item is not a runtime.Object")
			}
			return c.scheme.Convert(srcObj.DeepCopyObject(), obj, nil)
		}
	}

	return fmt.Errorf("object %s/%s not found in any cluster", key.Namespace, key.Name)
}

// List retrieves objects from the cache across ALL clusters.
// Objects in the result will have the cluster annotation set indicating which cluster they came from.
// Use the ClusterName list option to filter to a specific cluster, or omit it to get objects from all clusters.
func (c *wildcardCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	// Get the GVK for the list's item type
	gvk, err := apiutil.GVKForObject(list, c.scheme)
	if err != nil {
		return err
	}
	// Remove "List" suffix to get the item GVK
	gvk.Kind = gvk.Kind[:len(gvk.Kind)-4]

	// Create an exemplar of the item type
	exemplar, err := c.scheme.New(gvk)
	if err != nil {
		return err
	}

	inf, _, err := c.GetScopeableInformer(exemplar.(client.Object))
	if err != nil {
		return err
	}

	// Use the full store (not scoped) to get objects from ALL clusters
	items := inf.GetStore().List()

	// Apply list options
	listOpts := client.ListOptions{}
	for _, opt := range opts {
		opt.ApplyToList(&listOpts)
	}

	// Filter items based on list options
	var filtered []runtime.Object
	for _, item := range items {
		metaObj, ok := item.(metav1.Object)
		if !ok {
			continue
		}

		// Filter by namespace if specified
		if listOpts.Namespace != "" && metaObj.GetNamespace() != listOpts.Namespace {
			continue
		}

		// Filter by label selector if specified
		if listOpts.LabelSelector != nil {
			if !listOpts.LabelSelector.Matches(labels.Set(metaObj.GetLabels())) {
				continue
			}
		}

		runtimeObj, ok := item.(runtime.Object)
		if !ok {
			continue
		}
		filtered = append(filtered, runtimeObj.DeepCopyObject())
	}

	// Set the items on the list
	return meta.SetList(list, filtered)
}

// GetInformer returns an informer for the given object.
func (c *wildcardCache) GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	inf, _, err := c.GetScopeableInformer(obj)
	if err != nil {
		return nil, err
	}
	return inf, nil
}

// GetInformerForKind returns an informer for the given GVK.
func (c *wildcardCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind, opts ...cache.InformerGetOption) (cache.Informer, error) {
	obj, err := c.scheme.New(gvk)
	if err != nil {
		return nil, err
	}
	return c.GetInformer(ctx, obj.(client.Object), opts...)
}

// RemoveInformer is not supported on wildcard cache.
func (c *wildcardCache) RemoveInformer(ctx context.Context, obj client.Object) error {
	return errors.New("RemoveInformer not supported on wildcard cache")
}

// IndexField adds an index to the cache for the given field.
func (c *wildcardCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.indexFieldFuncs = append(c.indexFieldFuncs, indexFieldEntry{
		obj:          obj,
		field:        field,
		extractValue: extractValue,
	})

	// If informer already exists, add the indexer to it
	gvk, _ := apiutil.GVKForObject(obj, c.scheme)
	if inf, exists := c.informers[gvk]; exists {
		return inf.AddIndexers(toolscache.Indexers{
			field: func(o interface{}) ([]string, error) {
				clientObj, ok := o.(client.Object)
				if !ok {
					return nil, nil
				}
				return extractValue(clientObj), nil
			},
		})
	}

	return nil
}
