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
	"fmt"

	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	mcacceptance "sigs.k8s.io/multicluster-runtime/pkg/acceptance"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Provider Clusters Acceptance", Ordered, func() {
	var provider *Provider
	var manager mcmanager.Manager
	var generateCluster mcacceptance.ClusterGenerator

	BeforeAll(func() {
		By("Creating a new provider", func() {
			provider = New()
		})

		By("Creating a new manager", func() {
			var err error
			manager, err = mcmanager.New(localCfg, provider, mcmanager.Options{})
			Expect(err).NotTo(HaveOccurred(), "Failed to create manager")
		})

		By("Defining the ClusterGenerator", func() {
			generateCluster = func(ctx context.Context, errHandler mcacceptance.ErrorHandler) (string, *rest.Config, error) {
				testenv := &envtest.Environment{}
				cfg, err := testenv.Start()
				if err != nil {
					return "", nil, fmt.Errorf("failed to start envtest: %w", err)
				}

				cl, err := cluster.New(cfg)
				if err != nil {
					return "", nil, fmt.Errorf("failed to create cluster: %w", err)
				}

				name := mcacceptance.RandomClusterName()
				if err := provider.Add(ctx, name, cl, manager); err != nil {
					return "", nil, fmt.Errorf("failed to add cluster to provider: %w", err)
				}
				go func() {
					<-ctx.Done()
					errHandler(testenv.Stop())
				}()
				return name, cfg, nil
			}
		})
	})

	It("Should run the acceptance tests", func() {
		mcacceptance.Provider(GinkgoTB(), generateCluster, manager)
	})

})
