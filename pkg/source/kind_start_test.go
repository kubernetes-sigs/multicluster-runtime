/*
Copyright 2026 The Kubernetes Authors.

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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeInformer delivers events synchronously and, unlike
// controllertest.FakeInformer, really removes event handlers and
// accepts arbitrary delete payloads such as tombstones.
type fakeInformer struct {
	crcache.Informer

	lock     sync.Mutex
	handlers map[*fakeRegistration]toolscache.ResourceEventHandler
}

type fakeRegistration struct {
	toolscache.ResourceEventHandlerRegistration
}

func newFakeInformer() *fakeInformer {
	return &fakeInformer{handlers: map[*fakeRegistration]toolscache.ResourceEventHandler{}}
}

func (f *fakeInformer) AddEventHandlerWithResyncPeriod(h toolscache.ResourceEventHandler, _ time.Duration) (toolscache.ResourceEventHandlerRegistration, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	reg := &fakeRegistration{}
	f.handlers[reg] = h
	return reg, nil
}

func (f *fakeInformer) RemoveEventHandler(reg toolscache.ResourceEventHandlerRegistration) error {
	f.lock.Lock()
	defer f.lock.Unlock()
	delete(f.handlers, reg.(*fakeRegistration))
	return nil
}

func (f *fakeInformer) Registrations() int {
	f.lock.Lock()
	defer f.lock.Unlock()
	return len(f.handlers)
}

func (f *fakeInformer) each(fn func(toolscache.ResourceEventHandler)) {
	f.lock.Lock()
	hs := make([]toolscache.ResourceEventHandler, 0, len(f.handlers))
	for _, h := range f.handlers {
		hs = append(hs, h)
	}
	f.lock.Unlock()
	for _, h := range hs {
		fn(h)
	}
}

func (f *fakeInformer) add(obj any) {
	f.each(func(h toolscache.ResourceEventHandler) { h.OnAdd(obj, false) })
}
func (f *fakeInformer) update(oldObj, obj any) {
	f.each(func(h toolscache.ResourceEventHandler) { h.OnUpdate(oldObj, obj) })
}
func (f *fakeInformer) delete(obj any) {
	f.each(func(h toolscache.ResourceEventHandler) { h.OnDelete(obj) })
}

type fakeCache struct {
	crcache.Cache
	informer *fakeInformer
	unsynced bool
}

func (c *fakeCache) GetInformer(context.Context, client.Object, ...crcache.InformerGetOption) (crcache.Informer, error) {
	return c.informer, nil
}

func (c *fakeCache) WaitForCacheSync(ctx context.Context) bool {
	if c.unsynced {
		<-ctx.Done()
		return false
	}
	return true
}

type fakeCluster struct {
	cluster.Cluster
	cache *fakeCache
}

func (c *fakeCluster) GetCache() crcache.Cache { return c.cache }

func newFakeCluster() (*fakeCluster, *fakeInformer) {
	inf := newFakeInformer()
	return &fakeCluster{cache: &fakeCache{informer: inf}}, inf
}

func configMap(name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: name}}
}

func request(cl multicluster.ClusterName, name string) mcreconcile.Request {
	return mcreconcile.Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: name}},
		ClusterName: cl,
	}
}

func drain(q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) []mcreconcile.Request {
	var items []mcreconcile.Request
	for q.Len() > 0 {
		item, _ := q.Get()
		q.Done(item)
		items = append(items, item)
	}
	return items
}

var _ = Describe("kind Start", func() {
	var q workqueue.TypedRateLimitingInterface[mcreconcile.Request]

	BeforeEach(func() {
		q = workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
		DeferCleanup(q.ShutDown)
	})

	start := func(ctx context.Context, src TypedSyncingSource[*corev1.ConfigMap, mcreconcile.Request], name multicluster.ClusterName, cl cluster.Cluster) error {
		s, _, err := src.ForCluster(name, cl)
		Expect(err).NotTo(HaveOccurred())
		return s.Start(ctx, q)
	}

	It("should enqueue events with the name of the cluster they came from", func(ctx SpecContext) {
		src := Kind(&corev1.ConfigMap{}, mchandler.TypedEnqueueRequestForObject[*corev1.ConfigMap]())
		cl1, inf1 := newFakeCluster()
		cl2, inf2 := newFakeCluster()
		Expect(start(ctx, src, "c1", cl1)).To(Succeed())
		Expect(start(ctx, src, "c2", cl2)).To(Succeed())

		inf1.add(configMap("created"))
		inf2.update(configMap("updated"), configMap("updated"))
		inf1.delete(configMap("deleted"))

		Expect(drain(q)).To(ConsistOf(
			request("c1", "created"),
			request("c2", "updated"),
			request("c1", "deleted"),
		))
	})

	It("should apply predicates per event type", func(ctx SpecContext) {
		reject := predicate.TypedFuncs[*corev1.ConfigMap]{
			CreateFunc: func(event.TypedCreateEvent[*corev1.ConfigMap]) bool { return false },
			DeleteFunc: func(event.TypedDeleteEvent[*corev1.ConfigMap]) bool { return false },
		}
		src := Kind(&corev1.ConfigMap{}, mchandler.TypedEnqueueRequestForObject[*corev1.ConfigMap](), reject)
		cl, inf := newFakeCluster()
		Expect(start(ctx, src, "c1", cl)).To(Succeed())

		inf.add(configMap("created"))
		inf.update(configMap("updated"), configMap("updated"))
		inf.delete(configMap("deleted"))

		Expect(drain(q)).To(ConsistOf(request("c1", "updated")))
	})

	It("should enqueue deletes delivered as tombstones", func(ctx SpecContext) {
		src := Kind(&corev1.ConfigMap{}, mchandler.TypedEnqueueRequestForObject[*corev1.ConfigMap]())
		cl, inf := newFakeCluster()
		Expect(start(ctx, src, "c1", cl)).To(Succeed())

		inf.delete(toolscache.DeletedFinalStateUnknown{Key: "ns/gone", Obj: configMap("gone")})

		Expect(drain(q)).To(ConsistOf(request("c1", "gone")))
	})

	It("should remove its event handler when the context is cancelled", func(ctx SpecContext) {
		src := Kind(&corev1.ConfigMap{}, mchandler.TypedEnqueueRequestForObject[*corev1.ConfigMap]())
		cl, inf := newFakeCluster()
		srcCtx, cancel := context.WithCancel(ctx)
		Expect(start(srcCtx, src, "c1", cl)).To(Succeed())
		Expect(inf.Registrations()).To(Equal(1))

		cancel()
		Eventually(inf.Registrations).Should(BeZero())
		inf.add(configMap("late"))
		Expect(q.Len()).To(BeZero())
	})

	It("should keep exactly one event handler across restarts", func(ctx SpecContext) {
		src := Kind(&corev1.ConfigMap{}, mchandler.TypedEnqueueRequestForObject[*corev1.ConfigMap]())
		cl, inf := newFakeCluster()
		s, _, err := src.ForCluster("c1", cl)
		Expect(err).NotTo(HaveOccurred())

		By("starting twice with the same context")
		ctx1, cancel1 := context.WithCancel(ctx)
		Expect(s.Start(ctx1, q)).To(Succeed())
		Expect(s.Start(ctx1, q)).To(Succeed())
		Expect(inf.Registrations()).To(Equal(1))

		By("starting with a new context while the old one is live")
		ctx2, cancel2 := context.WithCancel(ctx)
		Expect(s.Start(ctx2, q)).To(Succeed())
		Expect(inf.Registrations()).To(Equal(1))

		By("cancelling the replaced context")
		cancel1()
		Consistently(inf.Registrations, "100ms").Should(Equal(1))
		inf.add(configMap("after-restart"))
		Expect(drain(q)).To(ConsistOf(request("c1", "after-restart")))

		By("cancelling the active context")
		cancel2()
		Eventually(inf.Registrations).Should(BeZero())
	})

	It("should fail when the handler func returns nil", func(ctx SpecContext) {
		src := Kind(&corev1.ConfigMap{}, mchandler.TypedEventHandlerFunc[*corev1.ConfigMap, mcreconcile.Request](
			func(multicluster.ClusterName, cluster.Cluster) handler.TypedEventHandler[*corev1.ConfigMap, mcreconcile.Request] {
				return nil
			}))
		cl, inf := newFakeCluster()

		Expect(start(ctx, src, "c1", cl)).NotTo(Succeed())
		Expect(inf.Registrations()).To(BeZero())
	})

	It("should remove its event handler when the cache does not sync", func(ctx SpecContext) {
		src := Kind(&corev1.ConfigMap{}, mchandler.TypedEnqueueRequestForObject[*corev1.ConfigMap]())
		cl, inf := newFakeCluster()
		cl.cache.unsynced = true
		srcCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()

		Expect(start(srcCtx, src, "c1", cl)).To(MatchError(context.DeadlineExceeded))
		Expect(inf.Registrations()).To(BeZero())
	})
})
