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
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mccache "sigs.k8s.io/multicluster-runtime/exp/informers/cache"
)

// ListerWatcherFactory creates ListerWatcher instances for a given GVK.
type ListerWatcherFactory interface {
	// Create creates a ListerWatcher for the given GVK that aggregates across all clusters.
	Create(gvk schema.GroupVersionKind, scheme *runtime.Scheme, clusters map[string]*rest.Config) toolscache.ListerWatcher
}

// MultiClusterListerWatcher aggregates List/Watch from multiple clusters.
// It uses the cluster annotation to track which cluster each object came from.
type MultiClusterListerWatcher struct {
	gvk      schema.GroupVersionKind
	scheme   *runtime.Scheme
	clusters map[string]*rest.Config
}

// NewMultiClusterListerWatcher creates a new MultiClusterListerWatcher.
func NewMultiClusterListerWatcher(gvk schema.GroupVersionKind, scheme *runtime.Scheme, clusters map[string]*rest.Config) *MultiClusterListerWatcher {
	return &MultiClusterListerWatcher{
		gvk:      gvk,
		scheme:   scheme,
		clusters: clusters,
	}
}

// List lists objects from all clusters and merges them.
func (m *MultiClusterListerWatcher) List(options metav1.ListOptions) (runtime.Object, error) {
	log := ctrl.Log.WithName("multi-cluster-lister-watcher")
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	// Create the list type
	listGVK := m.gvk.GroupVersion().WithKind(m.gvk.Kind + "List")
	listObj, err := m.scheme.New(listGVK)
	if err != nil {
		return nil, err
	}

	// Create a temporary list to collect items
	var allItems []runtime.Object

	for clusterName, cfg := range m.clusters {
		wg.Add(1)
		go func(name string, cfg *rest.Config) {
			defer wg.Done()

			c, err := client.New(cfg, client.Options{Scheme: m.scheme})
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}

			// Create a new list object for this cluster
			clusterListObj, err := m.scheme.New(listGVK)
			if err != nil {
				return
			}

			if err := c.List(context.TODO(), clusterListObj.(client.ObjectList)); err != nil {
				log.Error(err, "Failed to list objects", "cluster", name, "gvk", m.gvk)
				return
			}

			// Extract items and add cluster annotation
			items, err := extractItems(clusterListObj)
			if err != nil {
				return
			}

			mu.Lock()
			defer mu.Unlock()
			for _, item := range items {
				if metaObj, ok := item.(metav1.Object); ok {
					setClusterAnnotation(metaObj, name)
				}
				allItems = append(allItems, item)
			}
		}(clusterName, cfg)
	}

	wg.Wait()

	// Set items on the result list
	if err := setItems(listObj, allItems); err != nil {
		return nil, err
	}

	log.V(1).Info("Listed objects from all clusters", "gvk", m.gvk, "count", len(allItems))
	return listObj, nil
}

// Watch watches objects from all clusters and merges the events.
func (m *MultiClusterListerWatcher) Watch(options metav1.ListOptions) (watch.Interface, error) {
	log := ctrl.Log.WithName("multi-cluster-lister-watcher")
	log.V(1).Info("Starting watch", "gvk", m.gvk, "clusters", len(m.clusters))

	aggregator := newAggregatedWatcher()

	for clusterName, cfg := range m.clusters {
		go m.watchCluster(aggregator, clusterName, cfg, options)
	}

	return aggregator, nil
}

// watchCluster watches a single cluster and forwards events to the aggregator.
func (m *MultiClusterListerWatcher) watchCluster(aggregator *aggregatedWatcher, clusterName string, cfg *rest.Config, options metav1.ListOptions) {
	log := ctrl.Log.WithName("multi-cluster-lister-watcher").WithValues("cluster", clusterName, "gvk", m.gvk)

	for {
		select {
		case <-aggregator.stopCh:
			return
		default:
		}

		// Create dynamic client for this cluster
		dynClient, err := dynamic.NewForConfig(cfg)
		if err != nil {
			log.Error(err, "Failed to create dynamic client")
			time.Sleep(5 * time.Second)
			continue
		}

		// Get the GVR for this GVK
		gvr, err := m.getGVR(cfg)
		if err != nil {
			log.Error(err, "Failed to get GVR")
			time.Sleep(5 * time.Second)
			continue
		}

		// Start watching
		log.V(1).Info("Starting watch for cluster")
		watcher, err := dynClient.Resource(gvr).Watch(context.Background(), options)
		if err != nil {
			log.Error(err, "Failed to start watch")
			time.Sleep(5 * time.Second)
			continue
		}

		// Forward events
		m.forwardEvents(aggregator, watcher, clusterName, log)

		// If we get here, the watch ended - wait a bit and retry
		log.V(1).Info("Watch ended, restarting")
		time.Sleep(time.Second)
	}
}

// forwardEvents forwards events from a cluster watch to the aggregator.
func (m *MultiClusterListerWatcher) forwardEvents(aggregator *aggregatedWatcher, watcher watch.Interface, clusterName string, log logr.Logger) {
	defer watcher.Stop()

	for {
		select {
		case <-aggregator.stopCh:
			return
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return
			}

			// Convert unstructured to typed object if possible
			obj := event.Object
			if obj != nil {
				// Add cluster annotation
				if metaObj, ok := obj.(metav1.Object); ok {
					setClusterAnnotation(metaObj, clusterName)
				}

				// Try to convert to typed object
				if typed, err := m.convertToTyped(obj); err == nil {
					obj = typed
					// Re-add annotation after conversion
					if metaObj, ok := obj.(metav1.Object); ok {
						setClusterAnnotation(metaObj, clusterName)
					}
				}
			}

			// Forward the event
			select {
			case aggregator.resultCh <- watch.Event{Type: event.Type, Object: obj}:
				log.V(2).Info("Forwarded event", "type", event.Type)
			case <-aggregator.stopCh:
				return
			}
		}
	}
}

// getGVR returns the GroupVersionResource for this GVK.
func (m *MultiClusterListerWatcher) getGVR(cfg *rest.Config) (schema.GroupVersionResource, error) {
	// Simple pluralization - for production, use a proper REST mapper
	kind := m.gvk.Kind
	plural := strings.ToLower(kind) + "s"

	// Handle common irregular plurals
	switch strings.ToLower(kind) {
	case "endpoints":
		plural = "endpoints"
	case "ingress":
		plural = "ingresses"
	}

	return schema.GroupVersionResource{
		Group:    m.gvk.Group,
		Version:  m.gvk.Version,
		Resource: plural,
	}, nil
}

// convertToTyped converts an unstructured object to a typed object.
func (m *MultiClusterListerWatcher) convertToTyped(obj runtime.Object) (runtime.Object, error) {
	typed, err := m.scheme.New(m.gvk)
	if err != nil {
		return nil, err
	}

	// Use scheme conversion
	if err := m.scheme.Convert(obj, typed, nil); err != nil {
		return nil, err
	}

	return typed, nil
}

// aggregatedWatcher aggregates events from multiple cluster watches.
type aggregatedWatcher struct {
	resultCh chan watch.Event
	stopCh   chan struct{}
	stopped  bool
	mu       sync.Mutex
}

func newAggregatedWatcher() *aggregatedWatcher {
	return &aggregatedWatcher{
		resultCh: make(chan watch.Event, 100),
		stopCh:   make(chan struct{}),
	}
}

func (w *aggregatedWatcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.stopped {
		w.stopped = true
		close(w.stopCh)
	}
}

func (w *aggregatedWatcher) ResultChan() <-chan watch.Event {
	return w.resultCh
}

// setClusterAnnotation adds the cluster name annotation to an object.
func setClusterAnnotation(obj metav1.Object, clusterName string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[mccache.ClusterAnnotation] = clusterName
	obj.SetAnnotations(annotations)
}

// extractItems extracts items from a list object using reflection.
func extractItems(listObj runtime.Object) ([]runtime.Object, error) {
	items, err := meta.ExtractList(listObj)
	if err != nil {
		return nil, err
	}

	result := make([]runtime.Object, len(items))
	copy(result, items)
	return result, nil
}

// setItems sets items on a list object.
func setItems(listObj runtime.Object, items []runtime.Object) error {
	return meta.SetList(listObj, items)
}

// GetResourceVersion extracts the resource version from a list object.
func GetResourceVersion(listObj runtime.Object) (string, error) {
	accessor, err := meta.ListAccessor(listObj)
	if err != nil {
		return "", fmt.Errorf("failed to get list accessor: %w", err)
	}
	return accessor.GetResourceVersion(), nil
}
