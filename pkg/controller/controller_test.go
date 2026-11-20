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
	"runtime"
	"strings"
	"time"

	"k8s.io/client-go/util/workqueue"

	"sigs.k8s.io/controller-runtime/pkg/source"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// startWithin starts a source wrapped by startWithinContext for the engagement engCtx, as a controller running
// until ctlCtx ends would, and returns the context the source was started with.
func startWithin(engCtx, ctlCtx context.Context) context.Context {
	var srcCtx context.Context
	src := source.TypedFunc[mcreconcile.Request](func(ctx context.Context, _ workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
		srcCtx = ctx
		return nil
	})
	Expect(startWithinContext[mcreconcile.Request](engCtx, src).Start(ctlCtx, nil)).To(Succeed())
	Expect(srcCtx).NotTo(BeNil())
	return srcCtx
}

// goroutinesInStartWithinContext counts the goroutines running a function of startWithinContext. A frame's name may
// start with the caller's name, as in "controller.startWithin.startWithinContext[...].func2.1", when the call is inlined.
func goroutinesInStartWithinContext() int {
	var stacks string
	for size := 1 << 16; ; size *= 2 {
		buf := make([]byte, size)
		if n := runtime.Stack(buf, true); n < size {
			stacks = string(buf[:n])
			break
		}
	}
	count := 0
	for _, g := range strings.Split(stacks, "\n\n") {
		for _, frame := range strings.Split(g, "\n") {
			if strings.HasPrefix(frame, "sigs.k8s.io/multicluster-runtime/pkg/controller.") && strings.Contains(frame, "startWithinContext[") {
				count++
				break
			}
		}
	}
	return count
}

var _ = Describe("startWithinContext", func() {
	// A provider may end an engagement and engage the cluster again, for example to rebuild its cache after a watch
	// failure. Each engagement must not leave a goroutine behind for as long as the controller runs.
	It("should not leave a goroutine behind when the engagement ends", func(ctx SpecContext) {
		ctlCtx, stop := context.WithCancel(ctx)
		defer stop()
		engCtx, disengage := context.WithCancel(ctx)
		srcCtx := startWithin(engCtx, ctlCtx)
		Eventually(goroutinesInStartWithinContext).WithTimeout(5 * time.Second).Should(Equal(1))

		disengage()
		Eventually(srcCtx.Done()).Should(BeClosed())
		Eventually(goroutinesInStartWithinContext).WithTimeout(5 * time.Second).Should(BeZero())
	})

	It("should cancel the source when the controller stops", func(ctx SpecContext) {
		ctlCtx, stop := context.WithCancel(ctx)
		engCtx, disengage := context.WithCancel(ctx)
		defer disengage()
		srcCtx := startWithin(engCtx, ctlCtx)
		Expect(srcCtx.Done()).NotTo(BeClosed())

		stop()
		Eventually(srcCtx.Done()).WithTimeout(5 * time.Second).Should(BeClosed())
		Eventually(goroutinesInStartWithinContext).WithTimeout(5 * time.Second).Should(BeZero())
	})
})
