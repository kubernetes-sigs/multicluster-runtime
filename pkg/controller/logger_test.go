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
	"strings"
	"time"

	"github.com/go-logr/logr/funcr"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// controllerOptionsManager is a manager that only has controller options, which is all NewTypedUnmanaged reads.
type controllerOptionsManager struct {
	mcmanager.Manager
	options config.Controller
}

func (m controllerOptionsManager) GetControllerOptions() config.Controller {
	return m.options
}

var _ = Describe("mcController logger", func() {
	// controller-runtime's default logger only recognizes reconcile.Request, so the logger a reconciler of
	// mcreconcile.Request got from log.FromContext did not say which object it was reconciling.
	It("should add the request's object to the logger a reconciler gets", func(ctx SpecContext) {
		reconcileLogs := make(chan string, 1)
		logger := funcr.New(func(_, args string) {
			if strings.Contains(args, `"msg"="reconciling"`) {
				reconcileLogs <- args
			}
		}, funcr.Options{})

		mgr := controllerOptionsManager{options: config.Controller{Logger: logger, SkipNameValidation: ptr.To(true)}}
		c, err := NewTypedUnmanaged[mcreconcile.Request]("logger", mgr, Options{
			Reconciler: reconcile.TypedFunc[mcreconcile.Request](func(ctx context.Context, _ mcreconcile.Request) (reconcile.Result, error) {
				log.FromContext(ctx).Info("reconciling")
				return reconcile.Result{}, nil
			}),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Watch(source.TypedFunc[mcreconcile.Request](func(_ context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
			q.Add(mcreconcile.Request{
				Request:     reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "cm"}},
				ClusterName: "cluster-a",
			})
			return nil
		}))).To(Succeed())

		startCtx, stop := context.WithCancel(ctx)
		defer stop()
		errc := make(chan error, 1)
		go func() { errc <- c.Start(startCtx) }()

		var logged string
		Eventually(reconcileLogs).WithTimeout(5 * time.Second).Should(Receive(&logged))
		stop()
		Eventually(errc).WithTimeout(5 * time.Second).Should(Receive(BeNil()))
		Expect(logged).To(HavePrefix(`"level"=0 "msg"="reconciling" "controller"="logger" ` +
			`"object"={"name"="cm" "namespace"="default"} "namespace"="default" "name"="cm" "reconcileID"=`))
	})
})
