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

package controller

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	mcsource "sigs.k8s.io/multicluster-runtime/pkg/source"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordingSource counts its starts. With fail set it stands for a source on a cluster whose cache has stopped: a
// kind source's start then fails, as client-go refuses a handler on a stopped informer.
type recordingSource struct {
	fail   bool
	starts atomic.Int32
}

func (s *recordingSource) Start(context.Context, workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
	s.starts.Add(1)
	if s.fail {
		return errors.New("the cluster's cache has stopped")
	}
	return nil
}

// forEveryCluster engages src for every cluster.
type forEveryCluster struct{ src *recordingSource }

func (a forEveryCluster) ForCluster(multicluster.ClusterName, cluster.Cluster) (source.TypedSource[mcreconcile.Request], bool, error) {
	return a.src, true, nil
}

// notStarted returns an mcController whose controller has not started, as a standby's has not before it leads.
func notStarted(name string, src *recordingSource) *mcController[mcreconcile.Request] {
	c, err := controller.NewTypedUnmanaged[mcreconcile.Request](name, controller.TypedOptions[mcreconcile.Request]{
		Reconciler: reconcile.TypedFunc[mcreconcile.Request](func(context.Context, mcreconcile.Request) (reconcile.Result, error) {
			return reconcile.Result{}, nil
		}),
		SkipNameValidation: ptr.To(true),
	})
	Expect(err).NotTo(HaveOccurred())
	return &mcController[mcreconcile.Request]{
		TypedController: c,
		clusters:        map[multicluster.ClusterName]*engagedCluster{},
		sources:         []mcsource.TypedSource[client.Object, mcreconcile.Request]{forEveryCluster{src}},
	}
}

// startFor starts c until it has run for d or returned, and returns what Start returned, nil while it runs.
func startFor(ctx context.Context, c *mcController[mcreconcile.Request], d time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- c.Start(ctx) }()
	select {
	case err := <-errc:
		return err
	case <-time.After(d):
		return nil
	}
}

var _ = Describe("mcController Start", func() {
	// A cluster disengaged before the controller starts must not have its source started then: its cache has stopped,
	// so the start fails and the controller's Start fails with it, which stops a manager at a leader-election takeover.
	It("should not start the source of a cluster disengaged before the controller starts", func(ctx SpecContext) {
		src := &recordingSource{fail: true}
		c := notStarted("disengaged-before-start", src)
		engCtx, disengage := context.WithCancel(ctx)
		Expect(c.Engage(engCtx, "gone", nil)).To(Succeed())
		disengage()
		Eventually(func() bool {
			c.lock.Lock()
			defer c.lock.Unlock()
			_, engaged := c.clusters["gone"]
			return engaged
		}).WithTimeout(5 * time.Second).Should(BeFalse())

		Expect(startFor(ctx, c, 2*time.Second)).To(Succeed())
		Expect(src.starts.Load()).To(BeZero())
	})

	It("should start the source of a cluster still engaged when the controller starts", func(ctx SpecContext) {
		src := &recordingSource{}
		c := notStarted("engaged-at-start", src)
		engCtx, disengage := context.WithCancel(ctx)
		defer disengage()
		Expect(c.Engage(engCtx, "live", nil)).To(Succeed())

		Expect(startFor(ctx, c, 2*time.Second)).To(Succeed())
		Expect(src.starts.Load()).To(Equal(int32(1)))
	})
})
