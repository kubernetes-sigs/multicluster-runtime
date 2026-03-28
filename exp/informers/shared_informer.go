/*
Copyright 2015 The Kubernetes Authors.
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

package informers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/cache/synctrack"
	"k8s.io/klog/v2"
	"k8s.io/utils/buffer"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"

	mccache "sigs.k8s.io/multicluster-runtime/exp/informers/cache"
	mcreflector "sigs.k8s.io/multicluster-runtime/exp/informers/reflector"
)

const (
	// syncedPollPeriod controls how often you look at the status of your sync funcs
	syncedPollPeriod = 100 * time.Millisecond

	// initialBufferSize is the initial number of event notifications that can be buffered.
	initialBufferSize = 1024

	// minimumResyncPeriod is the minimum resync period for handlers.
	minimumResyncPeriod = 1 * time.Second
)

// NewMultiClusterSharedInformer creates a new instance for the ListerWatcher.
// See NewMultiClusterSharedIndexInformerWithOptions for full details.
func NewMultiClusterSharedInformer(lw cache.ListerWatcher, exampleObject runtime.Object, defaultEventHandlerResyncPeriod time.Duration) ScopeableSharedIndexInformer {
	return NewMultiClusterSharedIndexInformer(lw, exampleObject, defaultEventHandlerResyncPeriod, cache.Indexers{})
}

// NewMultiClusterSharedIndexInformer creates a new instance for the ListerWatcher and specified Indexers.
// See NewMultiClusterSharedIndexInformerWithOptions for full details.
func NewMultiClusterSharedIndexInformer(lw cache.ListerWatcher, exampleObject runtime.Object, defaultEventHandlerResyncPeriod time.Duration, indexers cache.Indexers) ScopeableSharedIndexInformer {
	return NewMultiClusterSharedIndexInformerWithOptions(
		lw,
		exampleObject,
		cache.SharedIndexInformerOptions{
			ResyncPeriod: defaultEventHandlerResyncPeriod,
			Indexers:     indexers,
		},
	)
}

// NewMultiClusterSharedIndexInformerWithOptions creates a new instance for the ListerWatcher.
// The created informer will not do resyncs if options.ResyncPeriod is zero. Otherwise: for each
// handler that with a non-zero requested resync period, whether added
// before or after the informer starts, the nominal resync period is
// the requested resync period rounded up to a multiple of the
// informer's resync checking period.
func NewMultiClusterSharedIndexInformerWithOptions(lw cache.ListerWatcher, exampleObject runtime.Object, options cache.SharedIndexInformerOptions) ScopeableSharedIndexInformer {
	realClock := &clock.RealClock{}

	// Add the cluster index to the indexers if not already present
	indexers := options.Indexers
	if indexers == nil {
		indexers = cache.Indexers{}
	}
	if _, exists := indexers[mccache.ClusterIndexName]; !exists {
		indexers[mccache.ClusterIndexName] = mccache.ClusterIndexFunc
	}

	return &sharedIndexInformer{
		// Use cluster-aware key function
		indexer:                         cache.NewIndexer(mccache.MetaClusterNamespaceKeyFunc, indexers),
		processor:                       &sharedProcessor{clock: realClock},
		listerWatcher:                   lw,
		objectType:                      exampleObject,
		objectDescription:               options.ObjectDescription,
		resyncCheckPeriod:               options.ResyncPeriod,
		defaultEventHandlerResyncPeriod: options.ResyncPeriod,
		clock:                           realClock,
		cacheMutationDetector:           cache.NewCacheMutationDetector(fmt.Sprintf("%T", exampleObject)),
	}
}

type updateNotification struct {
	oldObj interface{}
	newObj interface{}
}

type addNotification struct {
	newObj          interface{}
	isInInitialList bool
}

type deleteNotification struct {
	oldObj interface{}
}

// sharedIndexInformer implements ScopeableSharedIndexInformer and has three
// main components. One is an indexed local cache, `indexer cache.Indexer`.
// The second main component is a cache.Controller that pulls
// objects/notifications using the cache.ListerWatcher and pushes them into
// a DeltaFIFO --- whose knownObjects is the informer's local cache
// --- while concurrently Popping Deltas values from that fifo and
// processing them with `sharedIndexInformer::HandleDeltas`. Each
// invocation of HandleDeltas, which is done with the fifo's lock
// held, processes each Delta in turn. For each Delta this both
// updates the local cache and stuffs the relevant notification into
// the sharedProcessor. The third main component is that
// sharedProcessor, which is responsible for relaying those
// notifications to each of the informer's clients.
//
// This implementation uses cluster-aware keys to support multiple clusters
// feeding into a single shared informer.
type sharedIndexInformer struct {
	indexer    cache.Indexer
	controller cache.Controller

	processor             *sharedProcessor
	cacheMutationDetector cache.MutationDetector

	listerWatcher cache.ListerWatcher

	// objectType is an example object of the type this informer is expected to handle.
	objectType runtime.Object

	// objectDescription is the description of this informer's objects.
	objectDescription string

	// resyncCheckPeriod is how often we want the reflector's resync timer to fire.
	resyncCheckPeriod time.Duration

	// defaultEventHandlerResyncPeriod is the default resync period for handlers.
	defaultEventHandlerResyncPeriod time.Duration

	// clock allows for testability
	clock clock.Clock

	started, stopped bool
	startedLock      sync.Mutex

	// blockDeltas gives a way to stop all event distribution so that a late event handler
	// can safely join the shared informer.
	blockDeltas sync.Mutex

	// Called whenever the ListAndWatch drops the connection with an error.
	watchErrorHandler cache.WatchErrorHandlerWithContext

	transform cache.TransformFunc
}

var _ ScopeableSharedIndexInformer = &sharedIndexInformer{}

// Cluster returns a SharedIndexInformer scoped to a specific cluster.
func (s *sharedIndexInformer) Cluster(clusterName string) cache.SharedIndexInformer {
	return newScopedSharedIndexInformer(s, clusterName)
}

// ClusterWithContext returns a SharedIndexInformer scoped to a specific cluster.
// When the context is canceled, all handlers are unregistered.
func (s *sharedIndexInformer) ClusterWithContext(ctx context.Context, clusterName string) cache.SharedIndexInformer {
	return newScopedSharedIndexInformerWithContext(ctx, s, clusterName)
}

// dummyController hides the fact that a SharedInformer is different from a dedicated one
// where a caller can `Run`. The run method is disconnected in this case.
type dummyController struct {
	informer *sharedIndexInformer
}

func (v *dummyController) RunWithContext(context.Context) {
}

func (v *dummyController) Run(stopCh <-chan struct{}) {
}

func (v *dummyController) HasSynced() bool {
	return v.informer.HasSynced()
}

func (v *dummyController) LastSyncResourceVersion() string {
	if v.informer.controller == nil {
		return ""
	}
	return v.informer.controller.LastSyncResourceVersion()
}

func (s *sharedIndexInformer) SetWatchErrorHandler(handler cache.WatchErrorHandler) error {
	return s.SetWatchErrorHandlerWithContext(func(_ context.Context, r *cache.Reflector, err error) {
		handler(r, err)
	})
}

func (s *sharedIndexInformer) SetWatchErrorHandlerWithContext(handler cache.WatchErrorHandlerWithContext) error {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()

	if s.started {
		return fmt.Errorf("informer has already started")
	}

	s.watchErrorHandler = handler
	return nil
}

func (s *sharedIndexInformer) SetTransform(handler cache.TransformFunc) error {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()

	if s.started {
		return fmt.Errorf("informer has already started")
	}

	s.transform = handler
	return nil
}

func (s *sharedIndexInformer) Run(stopCh <-chan struct{}) {
	s.RunWithContext(wait.ContextForChannel(stopCh))
}

func (s *sharedIndexInformer) RunWithContext(ctx context.Context) {
	defer utilruntime.HandleCrashWithContext(ctx)
	logger := klog.FromContext(ctx)

	if s.HasStarted() {
		logger.Info("Warning: the sharedIndexInformer has started, run more than once is not allowed")
		return
	}

	func() {
		s.startedLock.Lock()
		defer s.startedLock.Unlock()

		// Create DeltaFIFO with cluster-aware key function
		fifo := cache.NewDeltaFIFOWithOptions(cache.DeltaFIFOOptions{
			KnownObjects:          s.indexer,
			EmitDeltaTypeReplaced: true,
			Transformer:           s.transform,
			// Use cluster-aware key function
			KeyFunction: mccache.MetaClusterNamespaceKeyFunc,
		})

		// Use our forked controller that passes the KeyFunction to the reflector.
		// This is critical for WatchList (client-go 1.34+) where the reflector creates a temporaryStore
		// that needs cluster-aware keys to avoid objects from different clusters overwriting each other.
		cfg := &mcreflector.Config{
			Queue:             fifo,
			ListerWatcher:     s.listerWatcher,
			ObjectType:        s.objectType,
			ObjectDescription: s.objectDescription,
			FullResyncPeriod:  s.resyncCheckPeriod,
			ShouldResync:      s.processor.shouldResync,

			Process:                      s.HandleDeltas,
			WatchErrorHandlerWithContext: s.watchErrorHandler,
			KeyFunction:                  mccache.DeletionHandlingMetaClusterNamespaceKeyFunc,
		}

		s.controller = mcreflector.New(cfg)
		s.started = true
	}()

	// Separate stop context because Processor should be stopped strictly after controller.
	processorStopCtx, stopProcessor := context.WithCancelCause(context.WithoutCancel(ctx))
	var wg wait.Group
	defer wg.Wait()
	defer stopProcessor(errors.New("informer is stopping"))
	wg.StartWithChannel(processorStopCtx.Done(), s.cacheMutationDetector.Run)
	wg.StartWithContext(processorStopCtx, s.processor.run)

	defer func() {
		s.startedLock.Lock()
		defer s.startedLock.Unlock()
		s.stopped = true
	}()
	s.controller.RunWithContext(ctx)
}

func (s *sharedIndexInformer) HasStarted() bool {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()
	return s.started
}

func (s *sharedIndexInformer) HasSynced() bool {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()

	if s.controller == nil {
		return false
	}
	return s.controller.HasSynced()
}

func (s *sharedIndexInformer) LastSyncResourceVersion() string {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()

	if s.controller == nil {
		return ""
	}
	return s.controller.LastSyncResourceVersion()
}

func (s *sharedIndexInformer) GetStore() cache.Store {
	return s.indexer
}

func (s *sharedIndexInformer) GetIndexer() cache.Indexer {
	return s.indexer
}

func (s *sharedIndexInformer) AddIndexers(indexers cache.Indexers) error {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()

	if s.stopped {
		return fmt.Errorf("indexer was not added because it has stopped already")
	}

	return s.indexer.AddIndexers(indexers)
}

func (s *sharedIndexInformer) GetController() cache.Controller {
	return &dummyController{informer: s}
}

func (s *sharedIndexInformer) AddEventHandler(handler cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	return s.AddEventHandlerWithOptions(handler, cache.HandlerOptions{})
}

func determineResyncPeriod(logger klog.Logger, desired, check time.Duration) time.Duration {
	if desired == 0 {
		return desired
	}
	if check == 0 {
		logger.Info("Warning: the specified resyncPeriod is invalid because this shared informer doesn't support resyncing", "desired", desired)
		return 0
	}
	if desired < check {
		logger.Info("Warning: the specified resyncPeriod is being increased to the minimum resyncCheckPeriod", "desired", desired, "resyncCheckPeriod", check)
		return check
	}
	return desired
}

func (s *sharedIndexInformer) AddEventHandlerWithResyncPeriod(handler cache.ResourceEventHandler, resyncPeriod time.Duration) (cache.ResourceEventHandlerRegistration, error) {
	return s.AddEventHandlerWithOptions(handler, cache.HandlerOptions{ResyncPeriod: &resyncPeriod})
}

func (s *sharedIndexInformer) AddEventHandlerWithOptions(handler cache.ResourceEventHandler, options cache.HandlerOptions) (cache.ResourceEventHandlerRegistration, error) {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()

	if s.stopped {
		return nil, fmt.Errorf("handler %v was not added to shared informer because it has stopped already", handler)
	}

	logger := ptr.Deref(options.Logger, klog.Background())
	resyncPeriod := ptr.Deref(options.ResyncPeriod, s.defaultEventHandlerResyncPeriod)
	if resyncPeriod > 0 {
		if resyncPeriod < minimumResyncPeriod {
			logger.Info("Warning: resync period is too small. Changing it to the minimum allowed value", "resyncPeriod", resyncPeriod, "minimumResyncPeriod", minimumResyncPeriod)
			resyncPeriod = minimumResyncPeriod
		}

		if resyncPeriod < s.resyncCheckPeriod {
			if s.started {
				logger.Info("Warning: resync period is smaller than resync check period and the informer has already started. Changing it to the resync check period", "resyncPeriod", resyncPeriod, "resyncCheckPeriod", s.resyncCheckPeriod)
				resyncPeriod = s.resyncCheckPeriod
			} else {
				s.resyncCheckPeriod = resyncPeriod
				s.processor.resyncCheckPeriodChanged(logger, resyncPeriod)
			}
		}
	}
	listener := newProcessListener(logger, handler, resyncPeriod, determineResyncPeriod(logger, resyncPeriod, s.resyncCheckPeriod), s.clock.Now(), initialBufferSize, s.HasSynced)

	if !s.started {
		return s.processor.addListener(listener), nil
	}

	// in order to safely join, we have to
	// 1. stop sending add/update/delete notifications
	// 2. do a list against the store
	// 3. send synthetic "Add" events to the new handler
	// 4. unblock
	s.blockDeltas.Lock()
	defer s.blockDeltas.Unlock()

	handle := s.processor.addListener(listener)
	for _, item := range s.indexer.List() {
		listener.add(addNotification{newObj: item, isInInitialList: true})
	}

	return handle, nil
}

func (s *sharedIndexInformer) HandleDeltas(obj interface{}, isInInitialList bool) error {
	s.blockDeltas.Lock()
	defer s.blockDeltas.Unlock()

	if deltas, ok := obj.(cache.Deltas); ok {
		return processDeltas(s, s.indexer, deltas, isInInitialList)
	}
	return errors.New("object given as Process argument is not Deltas")
}

// OnAdd conforms to cache.ResourceEventHandler
func (s *sharedIndexInformer) OnAdd(obj interface{}, isInInitialList bool) {
	s.cacheMutationDetector.AddObject(obj)
	s.processor.distribute(addNotification{newObj: obj, isInInitialList: isInInitialList}, false)
}

// OnUpdate conforms to cache.ResourceEventHandler
func (s *sharedIndexInformer) OnUpdate(old, new interface{}) {
	isSync := false

	if accessor, err := meta.Accessor(new); err == nil {
		if oldAccessor, err := meta.Accessor(old); err == nil {
			isSync = accessor.GetResourceVersion() == oldAccessor.GetResourceVersion()
		}
	}

	s.cacheMutationDetector.AddObject(new)
	s.processor.distribute(updateNotification{oldObj: old, newObj: new}, isSync)
}

// OnDelete conforms to cache.ResourceEventHandler
func (s *sharedIndexInformer) OnDelete(old interface{}) {
	s.processor.distribute(deleteNotification{oldObj: old}, false)
}

// IsStopped reports whether the informer has already been stopped
func (s *sharedIndexInformer) IsStopped() bool {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()
	return s.stopped
}

func (s *sharedIndexInformer) RemoveEventHandler(handle cache.ResourceEventHandlerRegistration) error {
	s.startedLock.Lock()
	defer s.startedLock.Unlock()

	s.blockDeltas.Lock()
	defer s.blockDeltas.Unlock()
	return s.processor.removeListener(handle)
}

// sharedProcessor has a collection of processorListener and can
// distribute a notification object to its listeners.
type sharedProcessor struct {
	listenersStarted bool
	listenersLock    sync.RWMutex
	// Map from listeners to whether or not they are currently syncing
	listeners map[*processorListener]bool
	clock     clock.Clock
	wg        wait.Group
}

func (p *sharedProcessor) getListener(registration cache.ResourceEventHandlerRegistration) *processorListener {
	p.listenersLock.RLock()
	defer p.listenersLock.RUnlock()

	if p.listeners == nil {
		return nil
	}

	if result, ok := registration.(*processorListener); ok {
		if _, exists := p.listeners[result]; exists {
			return result
		}
	}

	return nil
}

func (p *sharedProcessor) addListener(listener *processorListener) cache.ResourceEventHandlerRegistration {
	p.listenersLock.Lock()
	defer p.listenersLock.Unlock()

	if p.listeners == nil {
		p.listeners = make(map[*processorListener]bool)
	}

	p.listeners[listener] = true

	if p.listenersStarted {
		p.wg.Start(listener.run)
		p.wg.Start(listener.pop)
	}

	return listener
}

func (p *sharedProcessor) removeListener(handle cache.ResourceEventHandlerRegistration) error {
	p.listenersLock.Lock()
	defer p.listenersLock.Unlock()

	listener, ok := handle.(*processorListener)
	if !ok {
		return fmt.Errorf("invalid key type %t", handle)
	} else if p.listeners == nil {
		return nil
	} else if _, exists := p.listeners[listener]; !exists {
		return nil
	}

	delete(p.listeners, listener)

	if p.listenersStarted {
		close(listener.addCh)
	}

	return nil
}

func (p *sharedProcessor) distribute(obj interface{}, sync bool) {
	p.listenersLock.RLock()
	defer p.listenersLock.RUnlock()

	for listener, isSyncing := range p.listeners {
		switch {
		case !sync:
			listener.add(obj)
		case isSyncing:
			listener.add(obj)
		default:
			// skipping a sync obj for a non-syncing listener
		}
	}
}

func (p *sharedProcessor) run(ctx context.Context) {
	func() {
		p.listenersLock.RLock()
		defer p.listenersLock.RUnlock()
		for listener := range p.listeners {
			p.wg.Start(listener.run)
			p.wg.Start(listener.pop)
		}
		p.listenersStarted = true
	}()
	<-ctx.Done()

	p.listenersLock.Lock()
	defer p.listenersLock.Unlock()
	for listener := range p.listeners {
		close(listener.addCh)
	}

	p.listeners = nil
	p.listenersStarted = false

	p.wg.Wait()
}

func (p *sharedProcessor) shouldResync() bool {
	p.listenersLock.Lock()
	defer p.listenersLock.Unlock()

	resyncNeeded := false
	now := p.clock.Now()
	for listener := range p.listeners {
		shouldResync := listener.shouldResync(now)
		p.listeners[listener] = shouldResync

		if shouldResync {
			resyncNeeded = true
			listener.determineNextResync(now)
		}
	}
	return resyncNeeded
}

func (p *sharedProcessor) resyncCheckPeriodChanged(logger klog.Logger, resyncCheckPeriod time.Duration) {
	p.listenersLock.RLock()
	defer p.listenersLock.RUnlock()

	for listener := range p.listeners {
		resyncPeriod := determineResyncPeriod(
			logger, listener.requestedResyncPeriod, resyncCheckPeriod)
		listener.setResyncPeriod(resyncPeriod)
	}
}

// processorListener relays notifications from a sharedProcessor to
// one cache.ResourceEventHandler.
type processorListener struct {
	logger klog.Logger
	nextCh chan interface{}
	addCh  chan interface{}

	handler cache.ResourceEventHandler

	syncTracker *synctrack.SingleFileTracker

	pendingNotifications buffer.RingGrowing

	requestedResyncPeriod time.Duration
	resyncPeriod          time.Duration
	nextResync            time.Time
	resyncLock            sync.Mutex
}

func (p *processorListener) HasSynced() bool {
	return p.syncTracker.HasSynced()
}

func newProcessListener(logger klog.Logger, handler cache.ResourceEventHandler, requestedResyncPeriod, resyncPeriod time.Duration, now time.Time, bufferSize int, hasSynced func() bool) *processorListener {
	ret := &processorListener{
		logger:                logger,
		nextCh:                make(chan interface{}),
		addCh:                 make(chan interface{}),
		handler:               handler,
		syncTracker:           &synctrack.SingleFileTracker{UpstreamHasSynced: hasSynced},
		pendingNotifications:  *buffer.NewRingGrowing(bufferSize),
		requestedResyncPeriod: requestedResyncPeriod,
		resyncPeriod:          resyncPeriod,
	}

	ret.determineNextResync(now)

	return ret
}

func (p *processorListener) add(notification interface{}) {
	if a, ok := notification.(addNotification); ok && a.isInInitialList {
		p.syncTracker.Start()
	}
	p.addCh <- notification
}

func (p *processorListener) pop() {
	defer utilruntime.HandleCrashWithLogger(p.logger)
	defer close(p.nextCh)

	var nextCh chan<- interface{}
	var notification interface{}
	for {
		select {
		case nextCh <- notification:
			var ok bool
			notification, ok = p.pendingNotifications.ReadOne()
			if !ok {
				nextCh = nil
			}
		case notificationToAdd, ok := <-p.addCh:
			if !ok {
				return
			}
			if notification == nil {
				notification = notificationToAdd
				nextCh = p.nextCh
			} else {
				p.pendingNotifications.WriteOne(notificationToAdd)
			}
		}
	}
}

func (p *processorListener) run() {
	sleepAfterCrash := false
	for next := range p.nextCh {
		if sleepAfterCrash {
			time.Sleep(time.Second)
		}
		func() {
			sleepAfterCrash = true
			defer utilruntime.HandleCrashWithLogger(p.logger)

			switch notification := next.(type) {
			case updateNotification:
				p.handler.OnUpdate(notification.oldObj, notification.newObj)
			case addNotification:
				p.handler.OnAdd(notification.newObj, notification.isInInitialList)
				if notification.isInInitialList {
					p.syncTracker.Finished()
				}
			case deleteNotification:
				p.handler.OnDelete(notification.oldObj)
			default:
				utilruntime.HandleErrorWithLogger(p.logger, nil, "unrecognized notification", "notificationType", fmt.Sprintf("%T", next))
			}
			sleepAfterCrash = false
		}()
	}
}

func (p *processorListener) shouldResync(now time.Time) bool {
	p.resyncLock.Lock()
	defer p.resyncLock.Unlock()

	if p.resyncPeriod == 0 {
		return false
	}

	return now.After(p.nextResync) || now.Equal(p.nextResync)
}

func (p *processorListener) determineNextResync(now time.Time) {
	p.resyncLock.Lock()
	defer p.resyncLock.Unlock()

	p.nextResync = now.Add(p.resyncPeriod)
}

func (p *processorListener) setResyncPeriod(resyncPeriod time.Duration) {
	p.resyncLock.Lock()
	defer p.resyncLock.Unlock()

	p.resyncPeriod = resyncPeriod
}

// processDeltas multiplexes updates in the form of a list of Deltas into a Store,
// and informs a given handler of events OnUpdate, OnAdd, OnDelete.
func processDeltas(
	handler cache.ResourceEventHandler,
	clientState cache.Store,
	deltas cache.Deltas,
	isInInitialList bool,
) error {
	for _, d := range deltas {
		obj := d.Object

		switch d.Type {
		case cache.Sync, cache.Replaced, cache.Added, cache.Updated:
			if old, exists, err := clientState.Get(obj); err == nil && exists {
				if err := clientState.Update(obj); err != nil {
					return err
				}
				handler.OnUpdate(old, obj)
			} else {
				if err := clientState.Add(obj); err != nil {
					return err
				}
				handler.OnAdd(obj, isInInitialList)
			}
		case cache.Deleted:
			if err := clientState.Delete(obj); err != nil {
				return err
			}
			handler.OnDelete(obj)
		}
	}
	return nil
}

// WaitForCacheSync waits for caches to populate. It returns true if it was successful, false
// if the controller should shutdown.
func WaitForCacheSync(stopCh <-chan struct{}, cacheSyncs ...func() bool) bool {
	err := wait.PollUntilContextCancel(wait.ContextForChannel(stopCh), syncedPollPeriod, true,
		func(context.Context) (bool, error) {
			for _, syncFunc := range cacheSyncs {
				if !syncFunc() {
					return false, nil
				}
			}
			return true, nil
		})
	return err == nil
}
