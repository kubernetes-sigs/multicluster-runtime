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
	"fmt"
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
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// ClusterFilterFunc is a function that filters clusters.
type ClusterFilterFunc func(clusterName string, cluster cluster.Cluster) bool

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

type clusterKind[object client.Object, request mcreconcile.ClusterAware[request]] struct {
	clusterName string
	cl          cluster.Cluster
	obj         object
	h           handler.TypedEventHandler[object, request]
	preds       []predicate.TypedPredicate[object]
	resync      time.Duration

	mu           sync.Mutex
	registration toolscache.ResourceEventHandlerRegistration
	activeCtx    context.Context
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

func (k *kind[object, request]) ForCluster(name string, cl cluster.Cluster) (crsource.TypedSource[request], bool, error) {
	obj, err := k.project(cl, k.obj)
	if err != nil {
		return nil, false, err
	}
	// A valid TypedSource must always be returned, even if it shouldn't
	// engage based on the filter to allow engaging with the local
	// cluster.
	shouldEngage := true
	if k.clusterFilter != nil {
		shouldEngage = k.clusterFilter(name, cl)
	}
	return &clusterKind[object, request]{
		clusterName: name,
		cl:          cl,
		obj:         obj,
		h:           k.handler(name, cl),
		preds:       k.predicates,
		resync:      k.resync,
	}, shouldEngage, nil
}

func (k *kind[object, request]) SyncingForCluster(name string, cl cluster.Cluster) (crsource.TypedSyncingSource[request], bool, error) {
	src, shouldEngage, err := k.ForCluster(name, cl)
	if err != nil {
		return nil, shouldEngage, err
	}
	return src.(crsource.TypedSyncingSource[request]), shouldEngage, nil
}

// WaitForSync satisfies TypedSyncingSource.
func (ck *clusterKind[object, request]) WaitForSync(ctx context.Context) error {
	if !ck.cl.GetCache().WaitForCacheSync(ctx) {
		return ctx.Err()
	}
	return nil
}

// Start registers a removable handler on the (scoped) informer and removes it on ctx.Done().
func (ck *clusterKind[object, request]) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[request]) error {
	log := log.FromContext(ctx).WithValues("cluster", ck.clusterName, "source", "kind")

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
					ck.h.Create(ctx, e, q)
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
					ck.h.Update(ctx, e, q)
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
					ck.h.Delete(ctx, e, q)
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
	timeoutDuration := 10 * time.Minute
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, timeoutDuration)
	defer timeoutCancel()
	if !ck.cl.GetCache().WaitForCacheSync(timeoutCtx) {
		ck.mu.Lock()
		_ = inf.RemoveEventHandler(ck.registration)
		ck.registration = nil
		ck.activeCtx = nil
		ck.mu.Unlock()
		log.V(1).Error(timeoutCtx.Err(), "cache not synced; handler removed")
		return fmt.Errorf("cache is not synced within timeout %q: %w", timeoutDuration, timeoutCtx.Err())
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
