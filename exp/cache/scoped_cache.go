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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	mccache "sigs.k8s.io/multicluster-runtime/exp/informers/cache"
)

// ScopedCache provides a filtered view of a WildcardCache for a single cluster.
// It implements cache.Cache and filters all operations to only see objects
// from the specified cluster.
type ScopedCache struct {
	base        WildcardCache
	clusterName string
	scheme      *runtime.Scheme
}

// NewScopedCache creates a new ScopedCache that provides a filtered view
// of the wildcard cache for the specified cluster.
func NewScopedCache(base WildcardCache, clusterName string, scheme *runtime.Scheme) cache.Cache {
	return &ScopedCache{
		base:        base,
		clusterName: clusterName,
		scheme:      scheme,
	}
}

// Start is not supported on scoped cache; start the wildcard cache instead.
func (c *ScopedCache) Start(ctx context.Context) error {
	return errors.New("scoped cache cannot be started; start the wildcard cache instead")
}

// WaitForCacheSync waits for all informers to sync.
func (c *ScopedCache) WaitForCacheSync(ctx context.Context) bool {
	return c.base.WaitForCacheSync(ctx)
}

// IndexField adds an index to the cache for the given field.
func (c *ScopedCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	return c.base.IndexField(ctx, obj, field, extractValue)
}

// Get retrieves an object from the cache for this cluster.
func (c *ScopedCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	inf, _, err := c.base.GetScopeableInformer(obj)
	if err != nil {
		return err
	}

	// Use the scoped informer's store with cluster-aware key
	scopedInf := inf.Cluster(c.clusterName)
	clusterKey := mccache.ToClusterAwareKey(c.clusterName, key.Namespace, key.Name)

	item, exists, err := scopedInf.GetStore().GetByKey(clusterKey)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("object %s/%s not found in cluster %s", key.Namespace, key.Name, c.clusterName)
	}

	// Copy the object
	srcObj, ok := item.(runtime.Object)
	if !ok {
		return fmt.Errorf("cached item is not a runtime.Object")
	}

	// Use scheme to convert
	return c.scheme.Convert(srcObj.DeepCopyObject(), obj, nil)
}

// List retrieves a list of objects from the cache for this cluster.
func (c *ScopedCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	// Get the GVK for the list's item type
	gvk, err := apiutil.GVKForObject(list, c.scheme)
	if err != nil {
		return err
	}
	// Remove "List" suffix to get the item GVK
	gvk.Kind = gvk.Kind[:len(gvk.Kind)-4]

	// Create an exemplar of the item type
	obj, err := c.scheme.New(gvk)
	if err != nil {
		return err
	}

	inf, _, err := c.base.GetScopeableInformer(obj.(client.Object))
	if err != nil {
		return err
	}

	// Use the scoped informer's store - it filters to this cluster
	scopedInf := inf.Cluster(c.clusterName)
	items := scopedInf.GetStore().List()

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
			if !listOpts.LabelSelector.Matches(labelSet(metaObj.GetLabels())) {
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

// GetInformer returns a scoped informer for the given object type.
func (c *ScopedCache) GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error) {
	inf, _, err := c.base.GetScopeableInformer(obj)
	if err != nil {
		return nil, err
	}
	// Use the Cluster() method from ScopeableSharedIndexInformer
	return inf.Cluster(c.clusterName), nil
}

// GetInformerForKind returns a scoped informer for the given GVK.
func (c *ScopedCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind, opts ...cache.InformerGetOption) (cache.Informer, error) {
	obj, err := c.scheme.New(gvk)
	if err != nil {
		return nil, err
	}
	return c.GetInformer(ctx, obj.(client.Object), opts...)
}

// RemoveInformer is not supported on scoped cache.
func (c *ScopedCache) RemoveInformer(ctx context.Context, obj client.Object) error {
	return errors.New("RemoveInformer not supported on scoped cache")
}

// labelSet is a simple wrapper to make a map[string]string implement labels.Labels.
type labelSet map[string]string

func (ls labelSet) Has(label string) bool {
	_, ok := ls[label]
	return ok
}

func (ls labelSet) Get(label string) string {
	return ls[label]
}

func (ls labelSet) Lookup(label string) (string, bool) {
	val, ok := ls[label]
	return val, ok
}
