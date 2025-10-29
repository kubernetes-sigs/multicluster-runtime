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

package cluster_registry

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type mockProvider struct {
	clusterNames []string
}

func (m *mockProvider) Get(_ context.Context, _ string) (cluster.Cluster, error) {
	return cluster.Cluster(nil), nil
}

func (m *mockProvider) IndexField(_ context.Context, _ client.Object, _ string, _ client.IndexerFunc) error {
	return nil
}

func (m *mockProvider) Start(ctx context.Context, mcAware multicluster.Aware) error {
	for _, clusterName := range m.clusterNames {
		err := mcAware.Engage(ctx, clusterName, cluster.Cluster(nil))
		if err != nil {
			return err
		}
	}
	return nil
}

var _ = Describe("Cluster registry should return cluster names", Ordered, func() {
	ctx, cancel := context.WithCancel(context.Background())
	g, ctx := errgroup.WithContext(ctx)
	registry := New()

	BeforeAll(func() {
		provider := &mockProvider{clusterNames: []string{"clusterA", "clusterB"}}
		var mgr mcmanager.Manager

		By("Creating a new manager", func() {
			mgrInstance, err := mcmanager.New(&rest.Config{}, provider, mcmanager.Options{})
			Expect(err).NotTo(HaveOccurred())
			mgr = mgrInstance
		})
		By("Register clusterRegistry in manager", func() {
			err := mgr.Add(registry)
			Expect(err).NotTo(HaveOccurred())
		})

		By("Starting the manager and provider", func() {
			g.Go(func() error { return mgr.Start(ctx) })
			g.Go(func() error { return provider.Start(ctx, mgr) })
		})
	})

	It("should have all cluster names", func(ctx context.Context) {
		Eventually(ctx, func(g Gomega) {
			knownClusters := registry.ClusterNames()
			g.Expect(knownClusters).To(ConsistOf([]string{"clusterA", "clusterB"}), "Expected cluster names")
			g.Expect(knownClusters).To(HaveLen(2), "Expected two elements")
		}).WithTimeout(3 * time.Second).WithPolling(200 * time.Millisecond).Should(Succeed())
	})

	AfterAll(func() {
		cancel()
	})

})
