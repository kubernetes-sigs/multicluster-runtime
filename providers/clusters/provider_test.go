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

package clusters

import (
	"context"
	"errors"

	"golang.org/x/sync/errgroup"

	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

var _ = Describe("Provider Clusters", Ordered, func() {
	ctx, cancel := context.WithCancel(context.Background())
	g, ctx := errgroup.WithContext(ctx)

	var provider *Provider
	var manager mcmanager.Manager

	BeforeAll(func() {
		By("Creating a new provider", func() {
			provider = New()
		})

		By("Creating a new manager", func() {
			var err error
			manager, err = mcmanager.New(localCfg, provider, mcmanager.Options{})
			Expect(err).NotTo(HaveOccurred(), "Failed to create manager")
		})

		By("Starting the manager", func() {
			g.Go(func() error {
				return ignoreCanceled(manager.Start(ctx))
			})
		})
	})

	It("Should not have any clusters initially", func(ctx context.Context) {
		knownClusters := provider.ClusterNames()
		Expect(knownClusters).To(BeEmpty(), "Expected no clusters to be known initially")
	})

	It("Should offer a new cluster", func(ctx context.Context) {
		var cfg *rest.Config
		var cl cluster.Cluster
		By("By adding a new cluster to the provider", func() {
			// Start a new cluster using envtest.
			// This depends on the kube server used for testing, but
			// envtest is generally a good choice.
			localEnv := &envtest.Environment{}
			var err error
			cfg, err = localEnv.Start()
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				err := localEnv.Stop()
				Expect(err).NotTo(HaveOccurred(), "Failed to stop envtest cluster")
			})

			// Create a cluster.Cluster for the new cluster
			cl, err = cluster.New(cfg)
			Expect(err).NotTo(HaveOccurred(), "Failed to create new cluster")

			// Add it to the provider
			err = provider.Add(ctx, "new-cluster", cl, manager)
			Expect(err).NotTo(HaveOccurred(), "Failed to add new cluster to provider")
		})

		// Now the manager has engaged the cluster and operators will be
		// able to interact with the cluster
		retrieved, err := manager.GetCluster(ctx, "new-cluster")
		Expect(err).NotTo(HaveOccurred(), "Failed to get new cluster from manager")
		Expect(retrieved).NotTo(BeNil(), "Expected new cluster to be returned")
		Expect(retrieved).To(Equal(cl), "Expected cluster added to provider to be the same as the one retrieved from the manager")

		knownClusters := provider.ClusterNames()
		Expect(knownClusters).To(HaveLen(1), "Expected one cluster to be known")

		By("By removing the cluster from the provider", func() {
			// .Remove never errors and is safe to defer - if a cluster
			// exists it is disengaged and removed safely, if it does
			// not exist nothing happens.
			provider.Remove("new-cluster")
		})

		knownClusters = provider.ClusterNames()
		Expect(knownClusters).To(BeEmpty(), "Expected no clusters to be known after removal")
	})

	AfterAll(func() {
		By("Stopping the manager", func() {
			cancel()
			err := g.Wait()
			Expect(err).To(Succeed(), "Expected manager to stop without error")
		})
	})
})
