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
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("kind", func() {
	Describe("WithClusterFilter", func() {
		It("should not call handler when a cluster is filtered", func() {
			handlerCalled := false
			h := mchandler.TypedEventHandlerFunc[*corev1.ConfigMap, mcreconcile.Request](
				func(_ multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[*corev1.ConfigMap, mcreconcile.Request] {
					handlerCalled = true
					return nil
				},
			)

			src := Kind[*corev1.ConfigMap](&corev1.ConfigMap{}, h)

			src.WithClusterFilter(func(_ multicluster.ClusterName, _ cluster.Cluster) bool {
				return false
			})

			_, shouldEngage, err := src.ForCluster("filtered-cluster", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(shouldEngage).To(BeFalse())
			Expect(handlerCalled).To(BeFalse())
		})
	})
})
