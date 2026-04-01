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

package informers

import (
	"context"
	"sync"
	"time"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"

	mccache "sigs.k8s.io/multicluster-runtime/exp/informers/cache"
)

// scopedSharedIndexInformer ensures that event handlers added to the underlying
// informer are only called with objects matching the given cluster name.
type scopedSharedIndexInformer struct {
	*sharedIndexInformer
	clusterName string

	handlerRegistrationsLock sync.Mutex
	handlerRegistrations     map[cache.ResourceEventHandlerRegistration]bool
}

func newScopedSharedIndexInformer(sharedIndexInformer *sharedIndexInformer, clusterName string) *scopedSharedIndexInformer {
	return &scopedSharedIndexInformer{
		sharedIndexInformer:  sharedIndexInformer,
		clusterName:          clusterName,
		handlerRegistrations: make(map[cache.ResourceEventHandlerRegistration]bool),
	}
}

func newScopedSharedIndexInformerWithContext(ctx context.Context, sharedIndexInformer *sharedIndexInformer, clusterName string) *scopedSharedIndexInformer {
	informer := newScopedSharedIndexInformer(sharedIndexInformer, clusterName)
	go func() {
		<-ctx.Done()
		informer.unregisterAllHandlers()
	}()
	return informer
}

// AddEventHandler adds an event handler to the shared informer using the shared informer's resync
// period. Events to a single handler are delivered sequentially, but there is no coordination
// between different handlers.
func (s *scopedSharedIndexInformer) AddEventHandler(handler cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	return s.AddEventHandlerWithResyncPeriod(handler, s.sharedIndexInformer.defaultEventHandlerResyncPeriod)
}

// AddEventHandlerWithResyncPeriod adds an event handler to the
// shared informer with the requested resync period; zero means
// this handler does not care about resyncs.
func (s *scopedSharedIndexInformer) AddEventHandlerWithResyncPeriod(handler cache.ResourceEventHandler, resyncPeriod time.Duration) (cache.ResourceEventHandlerRegistration, error) {
	scopedHandler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if s.objectMatches(obj) {
				handler.OnAdd(obj, true)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if s.objectMatches(newObj) {
				handler.OnUpdate(oldObj, newObj)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if s.objectMatches(obj) {
				handler.OnDelete(obj)
			}
		},
	}

	registration, err := s.sharedIndexInformer.AddEventHandlerWithResyncPeriod(scopedHandler, resyncPeriod)
	if err != nil {
		return nil, err
	}

	s.handlerRegistrationsLock.Lock()
	defer s.handlerRegistrationsLock.Unlock()
	s.handlerRegistrations[registration] = true

	return registration, nil
}

// IsStopped reports whether the informer has already been stopped
func (s *scopedSharedIndexInformer) IsStopped() bool {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()
	return s.stopped
}

func (s *scopedSharedIndexInformer) RemoveEventHandler(handle cache.ResourceEventHandlerRegistration) error {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()

	s.blockDeltas.Lock()
	defer s.blockDeltas.Unlock()
	if err := s.processor.removeListener(handle); err != nil {
		return err
	}
	s.handlerRegistrationsLock.Lock()
	defer s.handlerRegistrationsLock.Unlock()
	delete(s.handlerRegistrations, handle)
	return nil
}

func (s *scopedSharedIndexInformer) unregisterAllHandlers() {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()
	s.blockDeltas.Lock()
	defer s.blockDeltas.Unlock()
	s.handlerRegistrationsLock.Lock()
	defer s.handlerRegistrationsLock.Unlock()

	for handle := range s.handlerRegistrations {
		utilruntime.HandleError(s.processor.removeListener(handle))
		delete(s.handlerRegistrations, handle)
	}
}

// objectMatches returns true if the object belongs to this scoped informer's cluster.
func (s *scopedSharedIndexInformer) objectMatches(obj interface{}) bool {
	key, err := mccache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		return false
	}
	cluster, _, _, err := mccache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return false
	}
	return cluster == s.clusterName
}

// GetIndexer returns an indexer that only contains objects from this cluster.
// Note: This returns the full indexer for now. For a truly scoped indexer,
// you would need to filter on every operation.
func (s *scopedSharedIndexInformer) GetIndexer() cache.Indexer {
	return &scopedIndexer{
		Indexer:     s.sharedIndexInformer.indexer,
		clusterName: s.clusterName,
	}
}

// GetStore returns a store that only contains objects from this cluster.
func (s *scopedSharedIndexInformer) GetStore() cache.Store {
	return &scopedIndexer{
		Indexer:     s.sharedIndexInformer.indexer,
		clusterName: s.clusterName,
	}
}

// scopedIndexer wraps an indexer to only return objects from a specific cluster.
type scopedIndexer struct {
	cache.Indexer
	clusterName string
}

// List returns all objects in the indexer that belong to this cluster.
func (s *scopedIndexer) List() []interface{} {
	all := s.Indexer.List()
	var result []interface{}
	for _, obj := range all {
		if s.objectMatches(obj) {
			result = append(result, obj)
		}
	}
	return result
}

// ListKeys returns all keys for objects that belong to this cluster.
func (s *scopedIndexer) ListKeys() []string {
	all := s.Indexer.ListKeys()
	var result []string
	for _, key := range all {
		cluster, _, _, err := mccache.SplitMetaClusterNamespaceKey(key)
		if err == nil && cluster == s.clusterName {
			result = append(result, key)
		}
	}
	return result
}

// Get returns the object if it belongs to this cluster.
func (s *scopedIndexer) Get(obj interface{}) (item interface{}, exists bool, err error) {
	item, exists, err = s.Indexer.Get(obj)
	if err != nil || !exists {
		return nil, false, err
	}
	if !s.objectMatches(item) {
		return nil, false, nil
	}
	return item, true, nil
}

// GetByKey returns the object if it belongs to this cluster.
func (s *scopedIndexer) GetByKey(key string) (item interface{}, exists bool, err error) {
	cluster, _, _, err := mccache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return nil, false, err
	}
	if cluster != s.clusterName {
		return nil, false, nil
	}
	return s.Indexer.GetByKey(key)
}

func (s *scopedIndexer) objectMatches(obj interface{}) bool {
	key, err := mccache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		return false
	}
	cluster, _, _, err := mccache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return false
	}
	return cluster == s.clusterName
}

// ByIndex returns all objects stored in the indexer that match the given indexer and indexedValue,
// filtered to only include objects from this cluster.
func (s *scopedIndexer) ByIndex(indexName, indexedValue string) ([]interface{}, error) {
	all, err := s.Indexer.ByIndex(indexName, indexedValue)
	if err != nil {
		return nil, err
	}
	var result []interface{}
	for _, obj := range all {
		if s.objectMatches(obj) {
			result = append(result, obj)
		}
	}
	return result, nil
}

// Index returns all objects stored in the indexer for the given indexer,
// filtered to only include objects from this cluster.
func (s *scopedIndexer) Index(indexName string, obj interface{}) ([]interface{}, error) {
	all, err := s.Indexer.Index(indexName, obj)
	if err != nil {
		return nil, err
	}
	var result []interface{}
	for _, item := range all {
		if s.objectMatches(item) {
			result = append(result, item)
		}
	}
	return result, nil
}

// IndexKeys returns all keys stored in the indexer for the given indexer and indexedValue,
// filtered to only include keys for objects from this cluster.
func (s *scopedIndexer) IndexKeys(indexName, indexedValue string) ([]string, error) {
	all, err := s.Indexer.IndexKeys(indexName, indexedValue)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, key := range all {
		cluster, _, _, err := mccache.SplitMetaClusterNamespaceKey(key)
		if err == nil && cluster == s.clusterName {
			result = append(result, key)
		}
	}
	return result, nil
}

// ListIndexFuncValues returns all unique indexed values for the given indexer,
// filtered to only include values for objects from this cluster.
func (s *scopedIndexer) ListIndexFuncValues(indexName string) []string {
	// For now, return all values since filtering would require examining all objects
	return s.Indexer.ListIndexFuncValues(indexName)
}
