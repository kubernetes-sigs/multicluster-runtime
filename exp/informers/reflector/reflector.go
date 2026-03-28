/*
Copyright 2014 The Kubernetes Authors.
Modifications Copyright 2025 The Kubernetes Authors.

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

package reflector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/naming"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	clientfeatures "k8s.io/client-go/features"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/pager"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"k8s.io/utils/trace"

	mccache "sigs.k8s.io/multicluster-runtime/exp/informers/cache"
)

// WatchErrorHandlerWithContext is called whenever ListAndWatch drops the
// connection with an error. After calling this handler, the informer
// will backoff and retry.
type WatchErrorHandlerWithContext func(ctx context.Context, r *Reflector, err error)

// DefaultWatchErrorHandler is the default implementation of WatchErrorHandlerWithContext.
func DefaultWatchErrorHandler(ctx context.Context, r *Reflector, err error) {
	switch {
	case isExpiredError(err):
		klog.FromContext(ctx).V(4).Info("Watch closed", "reflector", r.name, "type", r.typeDescription, "err", err)
	case errors.Is(err, io.EOF):
		// watch closed normally
	case errors.Is(err, io.ErrUnexpectedEOF):
		klog.FromContext(ctx).V(1).Info("Watch closed with unexpected EOF", "reflector", r.name, "type", r.typeDescription, "err", err)
	default:
		utilruntime.HandleErrorWithContext(ctx, err, "Failed to watch", "reflector", r.name, "type", r.typeDescription)
	}
}

const defaultExpectedTypeName = "<unspecified>"

// We try to spread the load on apiserver by setting timeouts for
// watch requests - it is random in [minWatchTimeout, 2*minWatchTimeout].
var defaultMinWatchTimeout = 5 * time.Minute

// Reflector watches a specified resource and causes all changes to be reflected in the given store.
// Added keyFunction field for cluster-aware key generation.
type Reflector struct {
	name            string
	typeDescription string
	expectedType    reflect.Type
	expectedGVK     *schema.GroupVersionKind
	store           cache.ReflectorStore
	listerWatcher   cache.ListerWatcherWithContext
	backoffManager  wait.BackoffManager
	resyncPeriod    time.Duration
	minWatchTimeout time.Duration
	clock           clock.Clock
	paginatedResult bool

	lastSyncResourceVersion              string
	isLastSyncResourceVersionUnavailable bool
	lastSyncResourceVersionMutex         sync.RWMutex

	watchErrorHandler             WatchErrorHandlerWithContext
	WatchListPageSize             int64
	ShouldResync                  func() bool
	MaxInternalErrorRetryDuration time.Duration
	useWatchList                  bool

	// keyFunction is used to generate keys for objects in the temporary store
	// during WatchList operations. This allows cluster-aware key generation for multi-cluster setups.
	keyFunction cache.KeyFunc
}

// Name returns the name of the reflector.
func (r *Reflector) Name() string {
	return r.name
}

// TypeDescription returns a description of the type of object this reflector is watching.
func (r *Reflector) TypeDescription() string {
	return r.typeDescription
}

// ReflectorOptions configures a Reflector.
type ReflectorOptions struct {
	Name            string
	TypeDescription string
	ResyncPeriod    time.Duration
	MinWatchTimeout time.Duration
	Clock           clock.Clock

	// KeyFunction is used to generate keys for objects in the temporary store
	// during WatchList operations. If unset, defaults to DeletionHandlingMetaClusterNamespaceKeyFunc
	// for cluster-aware key generation.
	KeyFunction cache.KeyFunc
}

// NewReflector creates a new Reflector with its name defaulted.
func NewReflector(lw cache.ListerWatcher, expectedType interface{}, store cache.ReflectorStore, resyncPeriod time.Duration) *Reflector {
	return NewReflectorWithOptions(lw, expectedType, store, ReflectorOptions{ResyncPeriod: resyncPeriod})
}

// NewNamedReflector creates a new Reflector with the specified name.
func NewNamedReflector(name string, lw cache.ListerWatcher, expectedType interface{}, store cache.ReflectorStore, resyncPeriod time.Duration) *Reflector {
	return NewReflectorWithOptions(lw, expectedType, store, ReflectorOptions{Name: name, ResyncPeriod: resyncPeriod})
}

// NewReflectorWithOptions creates a new Reflector object which will keep the
// given store up to date with the server's contents for the given resource.
func NewReflectorWithOptions(lw cache.ListerWatcher, expectedType interface{}, store cache.ReflectorStore, options ReflectorOptions) *Reflector {
	reflectorClock := options.Clock
	if reflectorClock == nil {
		reflectorClock = clock.RealClock{}
	}
	minWatchTimeout := defaultMinWatchTimeout
	if options.MinWatchTimeout > defaultMinWatchTimeout {
		minWatchTimeout = options.MinWatchTimeout
	}

	// Default to cluster-aware key function
	keyFunction := options.KeyFunction
	if keyFunction == nil {
		keyFunction = mccache.DeletionHandlingMetaClusterNamespaceKeyFunc
	}

	r := &Reflector{
		name:              options.Name,
		resyncPeriod:      options.ResyncPeriod,
		minWatchTimeout:   minWatchTimeout,
		typeDescription:   options.TypeDescription,
		listerWatcher:     cache.ToListerWatcherWithContext(lw),
		store:             store,
		backoffManager:    wait.NewExponentialBackoffManager(800*time.Millisecond, 30*time.Second, 2*time.Minute, 2.0, 1.0, reflectorClock),
		clock:             reflectorClock,
		watchErrorHandler: DefaultWatchErrorHandler,
		expectedType:      reflect.TypeOf(expectedType),
		keyFunction:       keyFunction,
	}

	if r.name == "" {
		r.name = naming.GetNameFromCallsite(internalPackages...)
	}

	if r.typeDescription == "" {
		r.typeDescription = getTypeDescriptionFromObject(expectedType)
	}

	if r.expectedGVK == nil {
		r.expectedGVK = getExpectedGVKFromObject(expectedType)
	}

	r.useWatchList = clientfeatures.FeatureGates().Enabled(clientfeatures.WatchListClient)

	return r
}

func getTypeDescriptionFromObject(expectedType interface{}) string {
	if expectedType == nil {
		return defaultExpectedTypeName
	}

	reflectDescription := reflect.TypeOf(expectedType).String()

	obj, ok := expectedType.(*unstructured.Unstructured)
	if !ok {
		return reflectDescription
	}

	gvk := obj.GroupVersionKind()
	if gvk.Empty() {
		return reflectDescription
	}

	return gvk.String()
}

func getExpectedGVKFromObject(expectedType interface{}) *schema.GroupVersionKind {
	obj, ok := expectedType.(*unstructured.Unstructured)
	if !ok {
		return nil
	}

	gvk := obj.GroupVersionKind()
	if gvk.Empty() {
		return nil
	}

	return &gvk
}

var internalPackages = []string{"client-go/tools/cache/", "multicluster-runtime/pkg/informers/reflector/"}

// Run repeatedly uses the reflector's ListAndWatch to fetch all the
// objects and subsequent deltas.
func (r *Reflector) Run(stopCh <-chan struct{}) {
	r.RunWithContext(wait.ContextForChannel(stopCh))
}

// RunWithContext repeatedly uses the reflector's ListAndWatch to fetch all the
// objects and subsequent deltas.
func (r *Reflector) RunWithContext(ctx context.Context) {
	logger := klog.FromContext(ctx)
	logger.V(3).Info("Starting reflector", "type", r.typeDescription, "resyncPeriod", r.resyncPeriod, "reflector", r.name)
	wait.BackoffUntil(func() {
		if err := r.ListAndWatchWithContext(ctx); err != nil {
			r.watchErrorHandler(ctx, r, err)
		}
	}, r.backoffManager, true, ctx.Done())
	logger.V(3).Info("Stopping reflector", "type", r.typeDescription, "resyncPeriod", r.resyncPeriod, "reflector", r.name)
}

var errorStopRequested = errors.New("stop requested")

// resyncChan returns a channel which will receive something when a resync is required.
func (r *Reflector) resyncChan() (<-chan time.Time, func() bool) {
	if r.resyncPeriod == 0 {
		return nil, func() bool { return false }
	}
	t := r.clock.NewTimer(r.resyncPeriod)
	return t.C(), t.Stop
}

// ListAndWatch first lists all items and get the resource version at the moment of call,
// and then use the resource version to watch.
func (r *Reflector) ListAndWatch(stopCh <-chan struct{}) error {
	return r.ListAndWatchWithContext(wait.ContextForChannel(stopCh))
}

// ListAndWatchWithContext first lists all items and get the resource version at the moment of call,
// and then use the resource version to watch.
func (r *Reflector) ListAndWatchWithContext(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.V(3).Info("Listing and watching", "type", r.typeDescription, "reflector", r.name)
	var err error
	var w watch.Interface
	fallbackToList := !r.useWatchList

	defer func() {
		if w != nil {
			w.Stop()
		}
	}()

	if r.useWatchList {
		w, err = r.watchList(ctx)
		if w == nil && err == nil {
			return nil
		}
		if err != nil {
			logger.V(4).Info(
				"Data couldn't be fetched in watchlist mode. Falling back to regular list.",
				"err", err,
			)
			fallbackToList = true
			w = nil
		}
	}

	if fallbackToList {
		err = r.list(ctx)
		if err != nil {
			return err
		}
	}

	logger.V(2).Info("Caches populated", "type", r.typeDescription, "reflector", r.name)
	return r.watchWithResync(ctx, w)
}

// startResync periodically calls r.store.Resync() method.
func (r *Reflector) startResync(ctx context.Context, resyncerrc chan error) {
	logger := klog.FromContext(ctx)
	resyncCh, cleanup := r.resyncChan()
	defer func() {
		cleanup()
	}()
	for {
		select {
		case <-resyncCh:
		case <-ctx.Done():
			return
		}
		if r.ShouldResync == nil || r.ShouldResync() {
			logger.V(4).Info("Forcing resync", "reflector", r.name)
			if err := r.store.Resync(); err != nil {
				resyncerrc <- err
				return
			}
		}
		cleanup()
		resyncCh, cleanup = r.resyncChan()
	}
}

// watchWithResync runs watch with startResync in the background.
func (r *Reflector) watchWithResync(ctx context.Context, w watch.Interface) error {
	resyncerrc := make(chan error, 1)
	cancelCtx, cancel := context.WithCancel(ctx)
	var wg wait.Group
	defer func() {
		cancel()
		wg.Wait()
	}()
	wg.Start(func() {
		r.startResync(cancelCtx, resyncerrc)
	})
	return r.watch(ctx, w, resyncerrc)
}

// watch starts a watch request with the server.
func (r *Reflector) watch(ctx context.Context, w watch.Interface, resyncerrc chan error) error {
	stopCh := ctx.Done()
	logger := klog.FromContext(ctx)
	var err error
	retry := cache.NewRetryWithDeadline(r.MaxInternalErrorRetryDuration, time.Minute, apierrors.IsInternalError, r.clock)
	defer func() {
		if w != nil {
			w.Stop()
		}
	}()

	for {
		select {
		case <-stopCh:
			return nil
		default:
		}

		start := r.clock.Now()

		if w == nil {
			timeoutSeconds := int64(r.minWatchTimeout.Seconds() * (rand.Float64() + 1.0))
			options := metav1.ListOptions{
				ResourceVersion:     r.LastSyncResourceVersion(),
				TimeoutSeconds:      &timeoutSeconds,
				AllowWatchBookmarks: true,
			}

			w, err = r.listerWatcher.WatchWithContext(ctx, options)
			if err != nil {
				if canRetry := isWatchErrorRetriable(err); canRetry {
					logger.V(4).Info("Watch failed - backing off", "reflector", r.name, "type", r.typeDescription, "err", err)
					select {
					case <-stopCh:
						return nil
					case <-r.backoffManager.Backoff().C():
						continue
					}
				}
				return err
			}
		}

		err = r.handleWatch(ctx, start, w, resyncerrc)
		w = nil
		retry.After(err)
		if err != nil {
			if !errors.Is(err, errorStopRequested) {
				switch {
				case isExpiredError(err):
					logger.V(4).Info("Watch closed", "reflector", r.name, "type", r.typeDescription, "err", err)
				case apierrors.IsTooManyRequests(err):
					logger.V(2).Info("Watch returned 429 - backing off", "reflector", r.name, "type", r.typeDescription)
					select {
					case <-stopCh:
						return nil
					case <-r.backoffManager.Backoff().C():
						continue
					}
				case apierrors.IsInternalError(err) && retry.ShouldRetry():
					logger.V(2).Info("Retrying watch after internal error", "reflector", r.name, "type", r.typeDescription, "err", err)
					continue
				default:
					logger.Info("Warning: watch ended with error", "reflector", r.name, "type", r.typeDescription, "err", err)
				}
			}
			return nil
		}
	}
}

// list simply lists all items and records a resource version.
func (r *Reflector) list(ctx context.Context) error {
	var resourceVersion string
	options := metav1.ListOptions{ResourceVersion: r.relistResourceVersion()}

	initTrace := trace.New("Reflector ListAndWatch", trace.Field{Key: "name", Value: r.name})
	defer initTrace.LogIfLong(10 * time.Second)
	var list runtime.Object
	var paginatedResult bool
	var err error
	listCh := make(chan struct{}, 1)
	panicCh := make(chan interface{}, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicCh <- r
			}
		}()
		pager := pager.New(pager.SimplePageFunc(func(opts metav1.ListOptions) (runtime.Object, error) {
			return r.listerWatcher.ListWithContext(ctx, opts)
		}))
		switch {
		case r.WatchListPageSize != 0:
			pager.PageSize = r.WatchListPageSize
		case r.paginatedResult:
		case options.ResourceVersion != "" && options.ResourceVersion != "0":
			pager.PageSize = 0
		}

		list, paginatedResult, err = pager.ListWithAlloc(context.Background(), options)
		if isExpiredError(err) || isTooLargeResourceVersionError(err) {
			r.setIsLastSyncResourceVersionUnavailable(true)
			list, paginatedResult, err = pager.ListWithAlloc(context.Background(), metav1.ListOptions{ResourceVersion: r.relistResourceVersion()})
		}
		close(listCh)
	}()
	select {
	case <-ctx.Done():
		return nil
	case p := <-panicCh:
		panic(p)
	case <-listCh:
	}
	initTrace.Step("Objects listed", trace.Field{Key: "error", Value: err})
	if err != nil {
		return fmt.Errorf("failed to list %v: %w", r.typeDescription, err)
	}

	if options.ResourceVersion == "0" && paginatedResult {
		r.paginatedResult = true
	}

	r.setIsLastSyncResourceVersionUnavailable(false)
	listMetaInterface, err := meta.ListAccessor(list)
	if err != nil {
		return fmt.Errorf("unable to understand list result %#v: %w", list, err)
	}
	resourceVersion = listMetaInterface.GetResourceVersion()
	initTrace.Step("Resource version extracted")
	items, err := meta.ExtractListWithAlloc(list)
	if err != nil {
		return fmt.Errorf("unable to understand list result %#v: %w", list, err)
	}
	initTrace.Step("Objects extracted")
	if err := r.syncWith(items, resourceVersion); err != nil {
		return fmt.Errorf("unable to sync list result: %w", err)
	}
	initTrace.Step("SyncWith done")
	r.setLastSyncResourceVersion(resourceVersion)
	initTrace.Step("Resource version updated")
	return nil
}

// watchList establishes a stream to get a consistent snapshot of data.
func (r *Reflector) watchList(ctx context.Context) (watch.Interface, error) {
	stopCh := ctx.Done()
	logger := klog.FromContext(ctx)
	var w watch.Interface
	var err error
	var temporaryStore cache.Store
	var resourceVersion string

	isErrorRetriableWithSideEffectsFn := func(err error) bool {
		if canRetry := isWatchErrorRetriable(err); canRetry {
			logger.V(2).Info("watch-list failed - backing off", "reflector", r.name, "type", r.typeDescription, "err", err)
			<-r.backoffManager.Backoff().C()
			return true
		}
		if isExpiredError(err) || isTooLargeResourceVersionError(err) {
			r.setIsLastSyncResourceVersionUnavailable(true)
			return true
		}
		return false
	}

	var transformer cache.TransformFunc
	storeOpts := []cache.StoreOption{}
	if tr, ok := r.store.(cache.TransformingStore); ok && tr.Transformer() != nil {
		transformer = tr.Transformer()
		storeOpts = append(storeOpts, cache.WithTransformer(transformer))
	}

	initTrace := trace.New("Reflector WatchList", trace.Field{Key: "name", Value: r.name})
	defer initTrace.LogIfLong(10 * time.Second)
	for {
		select {
		case <-stopCh:
			return nil, nil
		default:
		}

		resourceVersion = ""
		lastKnownRV := r.rewatchResourceVersion()
		// Use the configured keyFunction instead of DeletionHandlingMetaNamespaceKeyFunc.
		// This is critical for multi-cluster setups where objects from different clusters have the same
		// namespace/name but different cluster names. Without cluster-aware keys, objects overwrite each other.
		temporaryStore = cache.NewStore(r.keyFunction, storeOpts...)
		timeoutSeconds := int64(r.minWatchTimeout.Seconds() * (rand.Float64() + 1.0))
		options := metav1.ListOptions{
			ResourceVersion:      lastKnownRV,
			AllowWatchBookmarks:  true,
			SendInitialEvents:    ptr.To(true),
			ResourceVersionMatch: metav1.ResourceVersionMatchNotOlderThan,
			TimeoutSeconds:       &timeoutSeconds,
		}
		start := r.clock.Now()

		w, err = r.listerWatcher.WatchWithContext(ctx, options)
		if err != nil {
			if isErrorRetriableWithSideEffectsFn(err) {
				continue
			}
			return nil, err
		}
		watchListBookmarkReceived, err := r.handleListWatch(ctx, start, w, temporaryStore,
			func(rv string) { resourceVersion = rv },
			make(chan error))
		if err != nil {
			w.Stop()
			if errors.Is(err, errorStopRequested) {
				return nil, nil
			}
			if isErrorRetriableWithSideEffectsFn(err) {
				continue
			}
			return nil, err
		}
		if watchListBookmarkReceived {
			break
		}
	}
	initTrace.Step("Objects streamed", trace.Field{Key: "count", Value: len(temporaryStore.List())})
	r.setIsLastSyncResourceVersionUnavailable(false)

	if err := r.store.Replace(temporaryStore.List(), resourceVersion); err != nil {
		return nil, fmt.Errorf("unable to sync watch-list result: %w", err)
	}
	initTrace.Step("SyncWith done")
	r.setLastSyncResourceVersion(resourceVersion)

	return w, nil
}

// syncWith replaces the store's items with the given list.
func (r *Reflector) syncWith(items []runtime.Object, resourceVersion string) error {
	found := make([]interface{}, 0, len(items))
	for _, item := range items {
		found = append(found, item)
	}
	return r.store.Replace(found, resourceVersion)
}

// handleListWatch consumes events from w for watch-list.
func (r *Reflector) handleListWatch(
	ctx context.Context,
	start time.Time,
	w watch.Interface,
	store cache.Store,
	setLastSyncResourceVersion func(string),
	errCh chan error,
) (bool, error) {
	return r.handleAnyWatch(ctx, start, w, store, setLastSyncResourceVersion, true, errCh)
}

// handleWatch consumes events from w.
func (r *Reflector) handleWatch(
	ctx context.Context,
	start time.Time,
	w watch.Interface,
	errCh chan error,
) error {
	_, err := r.handleAnyWatch(ctx, start, w, r.store, r.setLastSyncResourceVersion, false, errCh)
	return err
}

// handleAnyWatch consumes events from w, updates the Store.
func (r *Reflector) handleAnyWatch(
	ctx context.Context,
	start time.Time,
	w watch.Interface,
	store cache.ReflectorStore,
	setLastSyncResourceVersion func(string),
	exitOnWatchListBookmarkReceived bool,
	errCh chan error,
) (bool, error) {
	watchListBookmarkReceived := false
	eventCount := 0
	logger := klog.FromContext(ctx)
	stopWatcher := true
	defer func() {
		if stopWatcher {
			w.Stop()
		}
	}()

loop:
	for {
		select {
		case <-ctx.Done():
			return watchListBookmarkReceived, errorStopRequested
		case err := <-errCh:
			return watchListBookmarkReceived, err
		case event, ok := <-w.ResultChan():
			if !ok {
				break loop
			}
			if event.Type == watch.Error {
				return watchListBookmarkReceived, apierrors.FromObject(event.Object)
			}
			if r.expectedType != nil {
				if e, a := r.expectedType, reflect.TypeOf(event.Object); e != a {
					utilruntime.HandleErrorWithContext(ctx, nil, "Unexpected watch event object type", "reflector", r.name, "expectedType", e, "actualType", a)
					continue
				}
			}
			if r.expectedGVK != nil {
				if e, a := *r.expectedGVK, event.Object.GetObjectKind().GroupVersionKind(); e != a {
					utilruntime.HandleErrorWithContext(ctx, nil, "Unexpected watch event object gvk", "reflector", r.name, "expectedGVK", e, "actualGVK", a)
					continue
				}
			}
			m, err := meta.Accessor(event.Object)
			if err != nil {
				utilruntime.HandleErrorWithContext(ctx, err, "Unable to understand watch event", "reflector", r.name, "event", event)
				continue
			}
			resourceVersion := m.GetResourceVersion()
			switch event.Type {
			case watch.Added:
				err := store.Add(event.Object)
				if err != nil {
					utilruntime.HandleErrorWithContext(ctx, err, "Unable to add watch event object to store", "reflector", r.name, "object", event.Object)
				}
			case watch.Modified:
				err := store.Update(event.Object)
				if err != nil {
					utilruntime.HandleErrorWithContext(ctx, err, "Unable to update watch event object to store", "reflector", r.name, "object", event.Object)
				}
			case watch.Deleted:
				err := store.Delete(event.Object)
				if err != nil {
					utilruntime.HandleErrorWithContext(ctx, err, "Unable to delete watch event object from store", "reflector", r.name, "object", event.Object)
				}
			case watch.Bookmark:
				if m.GetAnnotations()[metav1.InitialEventsAnnotationKey] == "true" {
					watchListBookmarkReceived = true
				}
			default:
				utilruntime.HandleErrorWithContext(ctx, nil, "Unknown watch event", "reflector", r.name, "event", event)
			}
			setLastSyncResourceVersion(resourceVersion)
			if rvu, ok := store.(cache.ResourceVersionUpdater); ok {
				rvu.UpdateResourceVersion(resourceVersion)
			}
			eventCount++
			if exitOnWatchListBookmarkReceived && watchListBookmarkReceived {
				stopWatcher = false
				watchDuration := r.clock.Since(start)
				logger.V(4).Info("Exiting watch because received the bookmark that marks the end of initial events stream", "reflector", r.name, "totalItems", eventCount, "duration", watchDuration)
				return watchListBookmarkReceived, nil
			}
		}
	}

	watchDuration := r.clock.Since(start)
	if watchDuration < 1*time.Second && eventCount == 0 {
		return watchListBookmarkReceived, fmt.Errorf("very short watch: %s: Unexpected watch close - watch lasted less than a second and no items received", r.name)
	}
	logger.V(4).Info("Watch close", "reflector", r.name, "type", r.typeDescription, "totalItems", eventCount)
	return watchListBookmarkReceived, nil
}

// LastSyncResourceVersion is the resource version observed when last sync with the underlying store.
func (r *Reflector) LastSyncResourceVersion() string {
	r.lastSyncResourceVersionMutex.RLock()
	defer r.lastSyncResourceVersionMutex.RUnlock()
	return r.lastSyncResourceVersion
}

func (r *Reflector) setLastSyncResourceVersion(v string) {
	r.lastSyncResourceVersionMutex.Lock()
	defer r.lastSyncResourceVersionMutex.Unlock()
	r.lastSyncResourceVersion = v
}

func (r *Reflector) relistResourceVersion() string {
	r.lastSyncResourceVersionMutex.RLock()
	defer r.lastSyncResourceVersionMutex.RUnlock()

	if r.isLastSyncResourceVersionUnavailable {
		return ""
	}
	if r.lastSyncResourceVersion == "" {
		return "0"
	}
	return r.lastSyncResourceVersion
}

func (r *Reflector) rewatchResourceVersion() string {
	r.lastSyncResourceVersionMutex.RLock()
	defer r.lastSyncResourceVersionMutex.RUnlock()
	if r.isLastSyncResourceVersionUnavailable {
		return ""
	}
	return r.lastSyncResourceVersion
}

func (r *Reflector) setIsLastSyncResourceVersionUnavailable(isUnavailable bool) {
	r.lastSyncResourceVersionMutex.Lock()
	defer r.lastSyncResourceVersionMutex.Unlock()
	r.isLastSyncResourceVersionUnavailable = isUnavailable
}

func isExpiredError(err error) bool {
	return apierrors.IsResourceExpired(err) || apierrors.IsGone(err)
}

func isTooLargeResourceVersionError(err error) bool {
	if apierrors.HasStatusCause(err, metav1.CauseTypeResourceVersionTooLarge) {
		return true
	}
	if !apierrors.IsTimeout(err) {
		return false
	}
	apierr, ok := err.(apierrors.APIStatus)
	if !ok || apierr == nil || apierr.Status().Details == nil {
		return false
	}
	for _, cause := range apierr.Status().Details.Causes {
		if cause.Message == "Too large resource version" {
			return true
		}
	}

	if strings.Contains(apierr.Status().Message, "Too large resource version") {
		return true
	}

	return false
}

func isWatchErrorRetriable(err error) bool {
	if utilnet.IsConnectionRefused(err) || apierrors.IsTooManyRequests(err) {
		return true
	}
	return false
}
