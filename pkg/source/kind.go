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

package source

import (
	"context"
	"sync"
	"time"

	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	crsource "sigs.k8s.io/controller-runtime/pkg/source"

	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// ClusterFilterFunc is a function that filters clusters.
type ClusterFilterFunc func(clusterName multicluster.ClusterName, cluster cluster.Cluster) bool

// Kind creates a KindSource with the given cache provider.
func Kind[object client.Object](
	obj object,
	handler mchandler.TypedEventHandlerFunc[object, mcreconcile.Request],
	predicates ...predicate.TypedPredicate[object],
) SyncingSource[object] {
	return TypedKind[object, mcreconcile.Request](obj, handler, predicates...)
}

// TypedKind creates a KindSource with the given cache provider.
func TypedKind[object client.Object, request mcreconcile.ClusterAware[request]](
	obj object,
	handler mchandler.TypedEventHandlerFunc[object, request],
	predicates ...predicate.TypedPredicate[object],
) TypedSyncingSource[object, request] {
	return &kind[object, request]{
		obj:        obj,
		handler:    handler,
		predicates: predicates,
		project:    func(_ cluster.Cluster, obj object) (object, error) { return obj, nil },
		resync:     0, // no periodic resync by default
	}
}

type kind[object client.Object, request mcreconcile.ClusterAware[request]] struct {
	obj           object
	handler       mchandler.TypedEventHandlerFunc[object, request]
	predicates    []predicate.TypedPredicate[object]
	project       func(cluster.Cluster, object) (object, error)
	resync        time.Duration
	clusterFilter ClusterFilterFunc
}

// clusterKind implements the per-cluster source for a specific kind.
// It handles the lifecycle of informer event handlers for a single cluster.
//
// The implementation uses lazy initialization and filtering to avoid unnecessary
// resource usage and side-effects:
//
//   - shouldEngage: This flag indicates whether the cluster should actually
//     participate in event handling based on the cluster filter. When false,
//     the source behaves as a no-op, returning immediately from WaitForSync()
//     and doing nothing in Start(). This prevents the creation of watches and
//     event handlers for filtered-out clusters while still satisfying the
//     TypedSyncingSource interface contract.
//
//   - handlerFunc: The event handler function provided by the user is stored
//     rather than being instantiated immediately. This avoids creating the
//     handler (which may have side-effects or expensive initialization) for
//     clusters that are filtered out. The actual handler.TypedEventHandler is
//     created lazily in getOrCreateHandler() only when needed and only for
//     engaging clusters.
//
//   - Efficiency: By deferring handler creation and skipping watches for filtered
//     clusters, we significantly reduce resource consumption in multi-cluster
//     environments where many clusters may be filtered out. This also prevents
//     any unexpected behavior that might arise from handler instantiation for
//     clusters that should be ignored.
//
// The struct maintains thread-safety through a mutex for all operations on
// registration and active context, ensuring proper cleanup during context
// cancellation and preventing race conditions.
type clusterKind[object client.Object, request mcreconcile.ClusterAware[request]] struct {
	// clusterName is the identifier of the cluster this source belongs to
	clusterName multicluster.ClusterName
	
	// cl is the cluster.Cluster instance providing access to the cluster's cache
	cl cluster.Cluster
	
	// obj is the projected object type this source watches
	obj object
	
	// handlerFunc is the user-provided event handler function that will be
	// lazily instantiated into handler.TypedEventHandler only for engaging clusters.
	// This lazy initialization prevents side-effects for filtered-out clusters.
	handlerFunc mchandler.TypedEventHandlerFunc[object, request]
	
	// preds are the predicates used to filter events before they reach the handler
	preds []predicate.TypedPredicate[object]
	
	// resync is the period for resyncing the informer
	resync time.Duration
	
	// shouldEngage indicates whether this cluster should actually participate
	// in event handling based on the cluster filter. When false, the source
	// becomes a no-op to avoid unnecessary watches and handler instantiation.
	// This flag is set during ForCluster() based on the cluster filter result.
	shouldEngage bool

	// mu protects concurrent access to registration, activeCtx, and handler
	mu sync.Mutex
	
	// registration is the handle for the registered event handler, used for cleanup
	registration toolscache.ResourceEventHandlerRegistration
	
	// activeCtx is the context with which the handler was last started,
	// used to track and clean up stale registrations
	activeCtx context.Context
	
	// handler is the lazily-instantiated event handler. It is created only when
	// needed (in Start()) and only for engaging clusters (shouldEngage=true).
	// This prevents side-effects and resource usage for filtered-out clusters.
	handler handler.TypedEventHandler[object, request]
}

// WithProjection sets the projection function for the KindSource.
func (k *kind[object, request]) WithProjection(project func(cluster.Cluster, object) (object, error)) TypedSyncingSource[object, request] {
	k.project = project
	return k
}

func (k *kind[object, request]) WithClusterFilter(filter ClusterFilterFunc) TypedSyncingSource[object, request] {
	k.clusterFilter = filter
	return k
}

func (k *kind[object, request]) ForCluster(name multicluster.ClusterName, cl cluster.Cluster) (crsource.TypedSource[request], bool, error) {
	obj, err := k.project(cl, k.obj)
	if err != nil {
		return nil, false, err
	}
	
	// Determine if this cluster should engage based on the filter
	shouldEngage := true
	if k.clusterFilter != nil {
		shouldEngage = k.clusterFilter(name, cl)
	}
	
	// Always return the same source type, but with engagement flag
	// Store the handler function instead of instantiating it to avoid
	// premature side-effects for filtered-out clusters
	return &clusterKind[object, request]{
		clusterName:  name,
		cl:           cl,
		obj:          obj,
		handlerFunc:  k.handler, // Store the function for lazy initialization
		preds:        k.predicates,
		resync:       k.resync,
		shouldEngage: shouldEngage,
	}, shouldEngage, nil
}

func (k *kind[object, request]) SyncingForCluster(name multicluster.ClusterName, cl cluster.Cluster) (crsource.TypedSyncingSource[request], bool, error) {
	src, shouldEngage, err := k.ForCluster(name, cl)
	if err != nil {
		return nil, shouldEngage, err
	}
	return src.(crsource.TypedSyncingSource[request]), shouldEngage, nil
}

// WaitForSync waits for the cluster's cache to sync.
// For non-engaging clusters (filtered out), it returns immediately with a log
// message to indicate that sync was skipped, without blocking controller startup.
// This satisfies the TypedSyncingSource interface while avoiding unnecessary
// waits and resource usage for filtered clusters.
func (ck *clusterKind[object, request]) WaitForSync(ctx context.Context) error {
	log := log.FromContext(ctx).WithValues("cluster", ck.clusterName, "source", "kind")
	
	// For non-engaging clusters, we're always "synced" from the perspective
	// of this source since we won't be creating any watches. Log at V(1) to
	// indicate this is happening without being too verbose.
	if !ck.shouldEngage {
		log.V(1).Info("cluster filtered out, skipping cache sync wait")
		return nil
	}
	
	// For engaging clusters, actually wait for the cache to sync
	if !ck.cl.GetCache().WaitForCacheSync(ctx) {
		return ctx.Err()
	}
	
	log.V(1).Info("cluster cache synced successfully")
	return nil
}

// getOrCreateHandler lazily creates the handler only when needed and only for engaging clusters.
// This method is thread-safe and ensures that:
//   - Handler is only instantiated once per clusterKind instance
//   - Handler is only created for clusters with shouldEngage=true
//   - No handler is created for filtered-out clusters, preventing side-effects
func (ck *clusterKind[object, request]) getOrCreateHandler() handler.TypedEventHandler[object, request] {
	ck.mu.Lock()
	defer ck.mu.Unlock()
	
	// Only instantiate the handler for engaging clusters and only if not already created
	if ck.handler == nil && ck.shouldEngage {
		// Lazy initialization: create the handler only when actually needed
		ck.handler = ck.handlerFunc(ck.clusterName, ck.cl)
	}
	return ck.handler
}

// Start registers a removable handler on the (scoped) informer and removes it on ctx.Done().
// For filtered-out clusters (shouldEngage=false), this method does nothing and returns nil,
// preventing any watch creation or handler instantiation.
func (ck *clusterKind[object, request]) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[request]) error {
	log := log.FromContext(ctx).WithValues("cluster", ck.clusterName, "source", "kind")
	
	// If this cluster should not engage, do nothing - no handler creation, no watches.
	// This is the key optimization that prevents resource usage and side-effects
	// for filtered-out clusters while maintaining the interface contract.
	if !ck.shouldEngage {
		log.V(1).Info("cluster filtered out, skipping source start")
		return nil
	}

	// Get or create the handler (lazy initialization)
	handler := ck.getOrCreateHandler()
	if handler == nil {
		// This shouldn't happen if shouldEngage is true, but log just in case
		log.V(1).Info("handler is nil for engaging cluster")
		return nil
	}

	// Check if we're already started with this context
	ck.mu.Lock()
	if ck.registration != nil && ck.activeCtx != nil {
		// Check if the active context is still valid
		select {
		case <-ck.activeCtx.Done():
			// Previous context cancelled, need to clean up and re-register
			log.V(1).Info("previous context cancelled, cleaning up for re-registration")
			// Clean up old registration is handled below
		default:
			// Still active with same context - check if it's the same context
			if ck.activeCtx == ctx {
				ck.mu.Unlock()
				log.V(1).Info("handler already registered with same context")
				return nil
			}
			// Different context but old one still active - this shouldn't happen
			log.V(1).Info("different context while old one active, will re-register")
		}
	}
	ck.mu.Unlock()

	inf, err := ck.getInformer(ctx, ck.obj)
	if err != nil {
		log.Error(err, "get informer failed")
		return err
	}

	// If there's an old registration, remove it first
	ck.mu.Lock()
	if ck.registration != nil {
		log.V(1).Info("removing old event handler registration")
		if err := inf.RemoveEventHandler(ck.registration); err != nil {
			log.Error(err, "failed to remove old event handler")
		}
		ck.registration = nil
		ck.activeCtx = nil
	}
	ck.mu.Unlock()

	// predicate helpers
	passCreate := func(e event.TypedCreateEvent[object]) bool {
		for _, p := range ck.preds {
			if !p.Create(e) {
				return false
			}
		}
		return true
	}
	passUpdate := func(e event.TypedUpdateEvent[object]) bool {
		for _, p := range ck.preds {
			if !p.Update(e) {
				return false
			}
		}
		return true
	}
	passDelete := func(e event.TypedDeleteEvent[object]) bool {
		for _, p := range ck.preds {
			if !p.Delete(e) {
				return false
			}
		}
		return true
	}

	// typed event builders
	makeCreate := func(o client.Object) event.TypedCreateEvent[object] {
		return event.TypedCreateEvent[object]{Object: any(o).(object)}
	}
	makeUpdate := func(oo, no client.Object) event.TypedUpdateEvent[object] {
		return event.TypedUpdateEvent[object]{ObjectOld: any(oo).(object), ObjectNew: any(no).(object)}
	}
	makeDelete := func(o client.Object) event.TypedDeleteEvent[object] {
		return event.TypedDeleteEvent[object]{Object: any(o).(object)}
	}

	// Adapter that forwards to controller handler, honoring ctx.
	h := toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(i interface{}) {
			if ctx.Err() != nil {
				return
			}
			if o, ok := i.(client.Object); ok {
				e := makeCreate(o)
				if passCreate(e) {
					handler.Create(ctx, e, q)
				}
			}
		},
		UpdateFunc: func(oo, no interface{}) {
			if ctx.Err() != nil {
				return
			}
			ooObj, ok1 := oo.(client.Object)
			noObj, ok2 := no.(client.Object)
			if ok1 && ok2 {
				e := makeUpdate(ooObj, noObj)
				if passUpdate(e) {
					handler.Update(ctx, e, q)
				}
			}
		},
		DeleteFunc: func(i interface{}) {
			if ctx.Err() != nil {
				return
			}
			// be robust to tombstones (provider should already unwrap)
			if ts, ok := i.(toolscache.DeletedFinalStateUnknown); ok {
				i = ts.Obj
			}
			if o, ok := i.(client.Object); ok {
				e := makeDelete(o)
				if passDelete(e) {
					handler.Delete(ctx, e, q)
				}
			}
		},
	}

	// Register via removable API.
	reg, addErr := inf.AddEventHandlerWithResyncPeriod(h, ck.resync)
	if addErr != nil {
		log.Error(addErr, "AddEventHandlerWithResyncPeriod failed")
		return addErr
	}

	// Store registration and context
	ck.mu.Lock()
	ck.registration = reg
	ck.activeCtx = ctx
	ck.mu.Unlock()

	log.V(1).Info("kind source handler registered", "hasRegistration", reg != nil)

	// Defensive: ensure cache is synced.
	if !ck.cl.GetCache().WaitForCacheSync(ctx) {
		ck.mu.Lock()
		_ = inf.RemoveEventHandler(ck.registration)
		ck.registration = nil
		ck.activeCtx = nil
		ck.mu.Unlock()
		log.V(1).Info("cache not synced; handler removed")
		return ctx.Err()
	}
	log.V(1).Info("kind source cache synced")

	// Wait for context cancellation in a goroutine
	go func() {
		<-ctx.Done()
		ck.mu.Lock()
		defer ck.mu.Unlock()

		// Only remove if this is still our active registration
		if ck.activeCtx == ctx && ck.registration != nil {
			if err := inf.RemoveEventHandler(ck.registration); err != nil {
				log.Error(err, "failed to remove event handler on context cancel")
			}
			ck.registration = nil
			ck.activeCtx = nil
			log.V(1).Info("kind source handler removed due to context cancellation")
		}
	}()

	return nil
}

// getInformer resolves the informer from the cluster cache (provider returns a scoped informer).
func (ck *clusterKind[object, request]) getInformer(ctx context.Context, obj client.Object) (crcache.Informer, error) {
	return ck.cl.GetCache().GetInformer(ctx, obj)
}