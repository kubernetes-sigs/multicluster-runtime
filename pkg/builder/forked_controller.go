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

package builder

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-logr/logr"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	mcsource "sigs.k8s.io/multicluster-runtime/pkg/source"
)

// project represents other forms that we can use to
// send/receive a given resource (metadata-only, unstructured, etc).
type objectProjection int

const (
	// projectAsNormal doesn't change the object from the form given.
	projectAsNormal objectProjection = iota
	// projectAsMetadata turns this into a metadata-only watch.
	projectAsMetadata
)

// Builder builds a Controller.
type Builder = TypedBuilder[mcreconcile.Request]

// TypedBuilder builds a Controller. The request is the request type
// that is passed to the workqueue and then to the Reconciler.
// The workqueue de-duplicates identical requests.
type TypedBuilder[request mcreconcile.ClusterAware[request]] struct {
	forInput         ForInput
	ownsInput        []OwnsInput
	rawSources       []source.TypedSource[request]
	watchesInput     []WatchesInput[request]
	mgr              mcmanager.Manager
	globalPredicates []predicate.Predicate
	ctrl             mccontroller.TypedController[request]
	ctrlOptions      controller.TypedOptions[request]
	name             string
	newController    func(name string, mgr mcmanager.Manager, options controller.TypedOptions[request]) (mccontroller.TypedController[request], error)

	enableClusterNotFoundWrapper *bool
}

// ControllerManagedBy returns a new controller builder that will be started by the provided Manager.
func ControllerManagedBy(m mcmanager.Manager) *Builder {
	return TypedControllerManagedBy[mcreconcile.Request](m)
}

// TypedControllerManagedBy returns a new typed controller builder that will be started by the provided Manager.
func TypedControllerManagedBy[request mcreconcile.ClusterAware[request]](m mcmanager.Manager) *TypedBuilder[request] {
	return &TypedBuilder[request]{mgr: m}
}

// ForInput represents the information set by the For method.
type ForInput struct {
	object           client.Object
	predicates       []predicate.Predicate
	objectProjection objectProjection
	err              error

	EngageOptions
}

// For defines the type of Object being *reconciled*, and configures the ControllerManagedBy to respond to create / delete /
// update events by *reconciling the object*.
//
// This is the equivalent of calling
// Watches(source.Kind(cache, &Type{}, &handler.EnqueueRequestForObject{})).
func (blder *TypedBuilder[request]) For(object client.Object, opts ...ForOption) *TypedBuilder[request] {
	if blder.forInput.object != nil {
		blder.forInput.err = fmt.Errorf("For(...) should only be called once, could not assign multiple objects for reconciliation")
		return blder
	}
	input := ForInput{object: object}
	for _, opt := range opts {
		opt.ApplyToFor(&input)
	}

	blder.forInput = input
	return blder
}

// OwnsInput represents the information set by Owns method.
type OwnsInput struct {
	matchEveryOwner  bool
	object           client.Object
	predicates       []predicate.Predicate
	objectProjection objectProjection

	EngageOptions
}

// Owns defines types of Objects being *generated* by the ControllerManagedBy, and configures the ControllerManagedBy to respond to
// create / delete / update events by *reconciling the owner object*.
//
// The default behavior reconciles only the first controller-type OwnerReference of the given type.
// Use Owns(object, builder.MatchEveryOwner) to reconcile all owners.
//
// By default, this is the equivalent of calling
// Watches(source.Kind(cache, &Type{}, handler.EnqueueRequestForOwner([...], &OwnerType{}, OnlyControllerOwner()))).
func (blder *TypedBuilder[request]) Owns(object client.Object, opts ...OwnsOption) *TypedBuilder[request] {
	input := OwnsInput{object: object}
	for _, opt := range opts {
		opt.ApplyToOwns(&input)
	}

	blder.ownsInput = append(blder.ownsInput, input)
	return blder
}

type untypedWatchesInput interface {
	setPredicates([]predicate.Predicate)
	setObjectProjection(objectProjection)
	setEngageWithLocalCluster(engage bool)
	setEngageWithProviderClusters(engage bool)
}

// WatchesInput represents the information set by Watches method.
type WatchesInput[request mcreconcile.ClusterAware[request]] struct {
	obj              client.Object
	handler          mchandler.TypedEventHandlerFunc[client.Object, request]
	predicates       []predicate.Predicate
	objectProjection objectProjection

	EngageOptions
}

func (w *WatchesInput[request]) setPredicates(predicates []predicate.Predicate) {
	w.predicates = predicates
}

func (w *WatchesInput[request]) setObjectProjection(objectProjection objectProjection) {
	w.objectProjection = objectProjection
}

// Watches defines the type of Object to watch, and configures the ControllerManagedBy to respond to create / delete /
// update events by *reconciling the object* with the given EventHandler.
//
// This is the equivalent of calling
// WatchesRawSource(source.Kind(cache, object, eventHandler, predicates...)).
func (blder *TypedBuilder[request]) Watches(
	object client.Object,
	eventHandler mchandler.TypedEventHandlerFunc[client.Object, request],
	opts ...WatchesOption,
) *TypedBuilder[request] {
	input := WatchesInput[request]{
		obj:     object,
		handler: mchandler.WithLowPriorityWhenUnchanged[client.Object, request](eventHandler),
	}
	for _, opt := range opts {
		opt.ApplyToWatches(&input)
	}

	blder.watchesInput = append(blder.watchesInput, input)

	return blder
}

// WatchesMetadata is the same as Watches, but forces the internal cache to only watch PartialObjectMetadata.
//
// This is useful when watching lots of objects, really big objects, or objects for which you only know
// the GVK, but not the structure. You'll need to pass metav1.PartialObjectMetadata to the client
// when fetching objects in your reconciler, otherwise you'll end up with a duplicate structured or unstructured cache.
//
// When watching a resource with metadata only, for example the v1.Pod, you should not Get and List using the v1.Pod type.
// Instead, you should use the special metav1.PartialObjectMetadata type.
//
// ❌ Incorrect:
//
//	pod := &v1.Pod{}
//	mgr.GetClient().Get(ctx, nsAndName, pod)
//
// ✅ Correct:
//
//	pod := &metav1.PartialObjectMetadata{}
//	pod.SetGroupVersionKind(schema.GroupVersionKind{
//	    Group:   "",
//	    Version: "v1",
//	    Kind:    "Pod",
//	})
//	mgr.GetClient().Get(ctx, nsAndName, pod)
//
// In the first case, controller-runtime will create another cache for the
// concrete type on top of the metadata cache; this increases memory
// consumption and leads to race conditions as caches are not in sync.
func (blder *TypedBuilder[request]) WatchesMetadata(
	object client.Object,
	eventHandler mchandler.TypedEventHandlerFunc[client.Object, request],
	opts ...WatchesOption,
) *TypedBuilder[request] {
	opts = append(opts, OnlyMetadata)
	return blder.Watches(object, eventHandler, opts...)
}

// WatchesRawSource exposes the lower-level ControllerManagedBy Watches functions through the builder.
//
// WatchesRawSource does not respect predicates configured through WithEventFilter.
//
// WatchesRawSource makes it possible to use typed handlers and predicates with `source.Kind` as well as custom source implementations.
func (blder *TypedBuilder[request]) WatchesRawSource(src source.TypedSource[request]) *TypedBuilder[request] {
	blder.rawSources = append(blder.rawSources, src)

	return blder
}

// WithEventFilter sets the event filters, to filter which create/update/delete/generic events eventually
// trigger reconciliations. For example, filtering on whether the resource version has changed.
// Given predicate is added for all watched objects and thus must be able to deal with the type
// of all watched objects.
//
// Defaults to the empty list.
func (blder *TypedBuilder[request]) WithEventFilter(p predicate.Predicate) *TypedBuilder[request] {
	blder.globalPredicates = append(blder.globalPredicates, p)
	return blder
}

// WithOptions overrides the controller options used in doController. Defaults to empty.
func (blder *TypedBuilder[request]) WithOptions(options controller.TypedOptions[request]) *TypedBuilder[request] {
	blder.ctrlOptions = options
	return blder
}

// WithLogConstructor overrides the controller options's LogConstructor.
func (blder *TypedBuilder[request]) WithLogConstructor(logConstructor func(*request) logr.Logger) *TypedBuilder[request] {
	blder.ctrlOptions.LogConstructor = logConstructor
	return blder
}

// WithClusterNotFoundWrapper enables or disables a reconciler that is wrapped around the original reconciler
// added to this builder. [reconcile.ClusterNotFoundWrapper] will stop reconcile results with [multicluster.ErrClusterNotFound]
// as error from requeuing by marking them as successfully reconciled. This wrapper is enabled by default
// and can be disabled with this builder method by setting it to false.
func (blder *TypedBuilder[request]) WithClusterNotFoundWrapper(enabled bool) *TypedBuilder[request] {
	blder.enableClusterNotFoundWrapper = ptr.To(enabled)
	return blder
}

// Named sets the name of the controller to the given name. The name shows up
// in metrics, among other things, and thus should be a prometheus compatible name
// (underscores and alphanumeric characters only).
//
// By default, controllers are named using the lowercase version of their kind.
//
// The name must be unique as it is used to identify the controller in metrics and logs.
func (blder *TypedBuilder[request]) Named(name string) *TypedBuilder[request] {
	blder.name = name
	return blder
}

// Complete builds the Application Controller.
//
// Note: use context.ReconcilerWithClusterInContext to inject the cluster name
// into the and to use Manager.GetClusterInContext to retrieve the cluster.
func (blder *TypedBuilder[request]) Complete(r reconcile.TypedReconciler[request]) error {
	_, err := blder.Build(r)
	return err
}

// Build builds the Application Controller and returns the Controller it created.
//
// Note: use context.ReconcilerWithClusterInContext to inject the cluster name
// into the and to use Manager.GetClusterInContext to retrieve the cluster.
func (blder *TypedBuilder[request]) Build(r reconcile.TypedReconciler[request]) (mccontroller.TypedController[request], error) {
	if r == nil {
		return nil, fmt.Errorf("must provide a non-nil Reconciler")
	}
	if blder.mgr == nil {
		return nil, fmt.Errorf("must provide a non-nil Manager")
	}
	if blder.forInput.err != nil {
		return nil, blder.forInput.err
	}

	// Set the ControllerManagedBy
	if err := blder.doController(r); err != nil {
		return nil, err
	}

	// Set the Watch
	if err := blder.doWatch(); err != nil {
		return nil, err
	}

	return blder.ctrl, nil
}

func (blder *TypedBuilder[request]) project(proj objectProjection) func(cluster.Cluster, client.Object) (client.Object, error) {
	return func(cl cluster.Cluster, obj client.Object) (client.Object, error) {
		switch proj {
		case projectAsNormal:
			return obj, nil
		case projectAsMetadata:
			metaObj := &metav1.PartialObjectMetadata{}
			gvk, err := apiutil.GVKForObject(obj, cl.GetScheme())
			if err != nil {
				return nil, fmt.Errorf("unable to determine GVK of %T for a metadata-only watch: %w", obj, err)
			}
			metaObj.SetGroupVersionKind(gvk)
			return metaObj, nil
		default:
			panic(fmt.Sprintf("unexpected projection type %v on type %T, should not be possible since this is an internal field", proj, obj))
		}
	}
}

func (blder *TypedBuilder[request]) doWatch() error {
	// Reconcile type
	if blder.forInput.object != nil {
		if reflect.TypeFor[request]() != reflect.TypeOf(mcreconcile.Request{}) {
			return fmt.Errorf("For() can only be used with mcreconcile.Request, got %T", *new(request))
		}

		var hdler mchandler.TypedEventHandlerFunc[client.Object, request]
		reflect.ValueOf(&hdler).Elem().Set(reflect.ValueOf(mchandler.WithLowPriorityWhenUnchanged(mchandler.EnqueueRequestForObject)))

		allPredicates := append([]predicate.Predicate(nil), blder.globalPredicates...)
		allPredicates = append(allPredicates, blder.forInput.predicates...)

		src := mcsource.TypedKind[client.Object, request](blder.forInput.object, hdler, allPredicates...).
			WithProjection(blder.project(blder.forInput.objectProjection))
		if ptr.Deref(blder.forInput.engageWithLocalCluster, blder.mgr.GetProvider() == nil) {
			src, err := src.ForCluster("", blder.mgr.GetLocalManager())
			if err != nil {
				return err
			}
			if err := blder.ctrl.Watch(src); err != nil {
				return err
			}
		}
		if ptr.Deref(blder.forInput.engageWithProviderClusters, blder.mgr.GetProvider() != nil) {
			if err := blder.ctrl.MultiClusterWatch(src); err != nil {
				return err
			}
		}
	}

	// Watches the managed types
	if len(blder.ownsInput) > 0 && blder.forInput.object == nil {
		return errors.New("Owns() can only be used together with For()")
	}
	for _, own := range blder.ownsInput {
		opts := []handler.OwnerOption{}
		if !own.matchEveryOwner {
			opts = append(opts, handler.OnlyControllerOwner())
		}

		hdler := func(clusterName string, cl cluster.Cluster) handler.TypedEventHandler[client.Object, request] {
			var hdler handler.TypedEventHandler[client.Object, request]
			reflect.ValueOf(&hdler).Elem().Set(reflect.ValueOf(mchandler.ForCluster(handler.WithLowPriorityWhenUnchanged(handler.EnqueueRequestForOwner(
				blder.mgr.GetLocalManager().GetScheme(), cl.GetRESTMapper(),
				blder.forInput.object,
				opts...,
			)), clusterName)))
			return hdler
		}
		allPredicates := append([]predicate.Predicate(nil), blder.globalPredicates...)
		allPredicates = append(allPredicates, own.predicates...)
		src := mcsource.TypedKind[client.Object, request](own.object, hdler, allPredicates...).
			WithProjection(blder.project(own.objectProjection))
		if ptr.Deref(own.engageWithLocalCluster, blder.mgr.GetProvider() == nil) {
			src, err := src.ForCluster("", blder.mgr.GetLocalManager())
			if err != nil {
				return err
			}
			if err := blder.ctrl.Watch(src); err != nil {
				return err
			}
		}
		if ptr.Deref(own.engageWithProviderClusters, blder.mgr.GetProvider() != nil) {
			if err := blder.ctrl.MultiClusterWatch(src); err != nil {
				return err
			}
		}
	}

	// Do the watch requests
	if len(blder.watchesInput) == 0 && blder.forInput.object == nil && len(blder.rawSources) == 0 {
		return errors.New("there are no watches configured, controller will never get triggered. Use For(), Owns(), Watches() or WatchesRawSource() to set them up")
	}
	for _, w := range blder.watchesInput {
		allPredicates := append([]predicate.Predicate(nil), blder.globalPredicates...)
		allPredicates = append(allPredicates, w.predicates...)
		src := mcsource.TypedKind[client.Object, request](w.obj, w.handler, allPredicates...).WithProjection(blder.project(w.objectProjection))
		if ptr.Deref(w.engageWithLocalCluster, blder.mgr.GetProvider() == nil) {
			src, err := src.ForCluster("", blder.mgr.GetLocalManager())
			if err != nil {
				return err
			}
			if err := blder.ctrl.Watch(src); err != nil {
				return err
			}
		}
		if ptr.Deref(w.engageWithProviderClusters, blder.mgr.GetProvider() != nil) {
			if err := blder.ctrl.MultiClusterWatch(src); err != nil {
				return err
			}
		}
	}
	for _, src := range blder.rawSources {
		if err := blder.ctrl.Watch(src); err != nil {
			return err
		}
	}
	return nil
}

func (blder *TypedBuilder[request]) getControllerName(gvk schema.GroupVersionKind, hasGVK bool) (string, error) {
	if blder.name != "" {
		return blder.name, nil
	}
	if !hasGVK {
		return "", errors.New("one of For() or Named() must be called")
	}
	return strings.ToLower(gvk.Kind), nil
}

func (blder *TypedBuilder[request]) doController(r reconcile.TypedReconciler[request]) error {
	globalOpts := blder.mgr.GetControllerOptions()

	ctrlOptions := blder.ctrlOptions
	if ctrlOptions.Reconciler != nil && r != nil {
		return errors.New("reconciler was set via WithOptions() and via Build() or Complete()")
	}
	if ctrlOptions.Reconciler == nil {
		ctrlOptions.Reconciler = r
	}

	// the ClusterNotFound wrapper is enabled by default, but can be disabled with WithClusterNotFoundWrapper(false).
	if ptr.Deref(blder.enableClusterNotFoundWrapper, true) {
		ctrlOptions.Reconciler = mcreconcile.NewClusterNotFoundWrapper(ctrlOptions.Reconciler)
	}

	// Retrieve the GVK from the object we're reconciling
	// to pre-populate logger information, and to optionally generate a default name.
	var gvk schema.GroupVersionKind
	hasGVK := blder.forInput.object != nil
	if hasGVK {
		var err error
		gvk, err = apiutil.GVKForObject(blder.forInput.object, blder.mgr.GetLocalManager().GetScheme())
		if err != nil {
			return err
		}
	}

	// Setup concurrency.
	if ctrlOptions.MaxConcurrentReconciles == 0 && hasGVK {
		groupKind := gvk.GroupKind().String()

		if concurrency, ok := globalOpts.GroupKindConcurrency[groupKind]; ok && concurrency > 0 {
			ctrlOptions.MaxConcurrentReconciles = concurrency
		}
	}

	// Setup cache sync timeout.
	if ctrlOptions.CacheSyncTimeout == 0 && globalOpts.CacheSyncTimeout > 0 {
		ctrlOptions.CacheSyncTimeout = globalOpts.CacheSyncTimeout
	}

	controllerName, err := blder.getControllerName(gvk, hasGVK)
	if err != nil {
		return err
	}

	// Setup the logger.
	if ctrlOptions.LogConstructor == nil {
		log := blder.mgr.GetLogger().WithValues(
			"controller", controllerName,
		)
		if hasGVK {
			log = log.WithValues(
				"controllerGroup", gvk.Group,
				"controllerKind", gvk.Kind,
			)
		}

		ctrlOptions.LogConstructor = func(in *request) logr.Logger {
			log := log

			if req, ok := any(in).(*reconcile.Request); ok && req != nil {
				if hasGVK {
					log = log.WithValues(gvk.Kind, klog.KRef(req.Namespace, req.Name))
				}
				log = log.WithValues(
					"namespace", req.Namespace, "name", req.Name,
				)
			}
			return log
		}
	}

	if blder.newController == nil {
		blder.newController = mccontroller.NewTyped[request]
	}

	// Build the controller and return.
	blder.ctrl, err = blder.newController(controllerName, blder.mgr, ctrlOptions)
	return err
}
