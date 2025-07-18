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

package file

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/randfill"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Provider File", Ordered, func() {
	ctx, cancel := context.WithCancel(context.Background())
	g, ctx := errgroup.WithContext(ctx)

	var discoverDir string
	var kubeconfigDir string
	var kubeconfigPath string
	var provider *Provider

	BeforeAll(func() {
		discoverDir = GinkgoT().TempDir()
		kubeconfigDir = GinkgoT().TempDir()
		kubeconfigPath = filepath.Join(kubeconfigDir, "non-descript-name")

		By("Creating a new provider", func() {
			var err error
			provider, err = New(Options{
				KubeconfigFiles: []string{kubeconfigPath},
				KubeconfigDirs:  []string{discoverDir},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		By("Starting the provider", func() {
			g.Go(func() error {
				return ignoreCanceled(provider.Run(ctx, nil))
			})
		})

		By("Creating a temporary directory for discovery", func() {
			err := os.MkdirAll(discoverDir, 0755)
			Expect(err).NotTo(HaveOccurred(), "Failed to create discovery directory")
		})

		By("Creating a temporary directory for kubeconfig files", func() {
			err := os.MkdirAll(kubeconfigDir, 0755)
			Expect(err).NotTo(HaveOccurred(), "Failed to create kubeconfig file")
		})

	})

	It("should not have any clusters initially", func(ctx context.Context) {
		knownClusters := provider.ClusterNames()
		Expect(knownClusters).To(BeEmpty(), "Expected no clusters to be known initially")
	})

	var directoryContexts []string
	It("should discover clusters from files inside the directories", func(ctx context.Context) {
		By("Creating a kubeconfig file in the test directory", func() {
			var err error
			directoryContexts, err = randomKubeconfig(1, filepath.Join(discoverDir, "kubeconfig.yaml"))
			Expect(err).NotTo(HaveOccurred(), "Failed to write kubeconfig file")
			Expect(directoryContexts).To(HaveLen(1), "Expected one kubeconfig file to be generated")
		})

		Eventually(provider.ClusterNames, "10s", "1s").Should(Equal(directoryContexts), "Expected provider to discover kubeconfig contexts from directory")
	})

	var kubeconfigContexts []string
	It("should discovery multiple clusters from passed filename", func(ctx context.Context) {
		By("Creating multiple kubeconfig files in the test directory", func() {
			var err error
			kubeconfigContexts, err = randomKubeconfig(2, kubeconfigPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to write kubeconfig files")
			Expect(kubeconfigContexts).To(HaveLen(2), "Expected two kubeconfig files to be generated")
		})

		Eventually(provider.ClusterNames, "10s", "1s").Should(ContainElements(kubeconfigContexts), "Expected provider to discover kubeconfig contexts")
	})

	It("should discover clusters from both directories", func(ctx context.Context) {
		Eventually(provider.ClusterNames, "10s", "1s").Should(ContainElements(directoryContexts), "Expected provider to discover clusters from directory")
		Eventually(provider.ClusterNames, "10s", "1s").Should(ContainElements(kubeconfigContexts), "Expected provider to discover clusters from kubeconfig files")
	})

	It("should remove clusters when the respective kubeconfig is deleted", func(ctx context.Context) {
		By("Deleting the kubeconfig file in the test directory", func() {
			err := os.Remove(kubeconfigPath)
			Expect(err).NotTo(HaveOccurred(), "Failed to remove kubeconfig file")
		})

		Eventually(provider.ClusterNames, "10s", "1s").ShouldNot(Equal(kubeconfigContexts), "Expected provider to remove kubeconfig contexts after file deletion")
	})

	var newDirectoryContexts []string
	It("should remove old clusters and add new ones when kubeconfig files are updated", func(ctx context.Context) {
		By("Updating the kubeconfig file in the test directory", func() {
			var err error
			newDirectoryContexts, err = randomKubeconfig(2, filepath.Join(discoverDir, "kubeconfig.yaml"))
			Expect(err).NotTo(HaveOccurred(), "Failed to write updated kubeconfig file")
			Expect(newDirectoryContexts).To(HaveLen(2), "Expected two new kubeconfig contexts to be generated")
		})

		Eventually(provider.ClusterNames, "10s", "1s").Should(ContainElements(newDirectoryContexts), "Expected provider to discover new kubeconfig contexts")
		Eventually(provider.ClusterNames, "10s", "1s").ShouldNot(ContainElements(directoryContexts), "Expected provider to remove old kubeconfig contexts")
	})

	AfterAll(func() {
		By("Stopping the provider", func() {
			cancel()
			err := g.Wait()
			Expect(err).To(Succeed(), "Expected provider to stop without error")
		})
	})
})

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// randomKubeconfig generates a kubeconfig file with n contexts and
// writes it to the specified path.
// The names of the contexts are returned.
func randomKubeconfig(n int, path string) ([]string, error) {
	cfg := clientcmdapi.NewConfig()
	contextNames := make([]string, n)

	filler := randfill.New()
	for i := 0; i < n; i++ {
		var contextName string
		ok := false
		for !ok {
			contextName, ok = randomKubeconfigContent(filler, cfg)
		}
		contextNames[i] = contextName
	}

	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		return nil, err
	}

	return contextNames, nil
}

func randomString(filler *randfill.Filler, length int) string {
	b := make([]byte, length)
	filler.Fill(&b)
	// Otherwise we might end up with a string that contains invalid
	// characters for a kubeconfig
	return base64.URLEncoding.EncodeToString(b)
}

// randomKubeconfigContent generates a random kubeconfig context, auth
// info, and cluster, returning the context name a bool indicating
// success.
func randomKubeconfigContent(filler *randfill.Filler, cfg *clientcmdapi.Config) (string, bool) {
	contextName := randomString(filler, 20)
	if cfg.Contexts[contextName] != nil {
		return "", false
	}

	authInfoName := randomString(filler, 20)
	if cfg.AuthInfos[authInfoName] != nil {
		return "", false
	}
	authInfo := clientcmdapi.NewAuthInfo()
	authInfo.Token = randomString(filler, 64) // Ensure the token is a valid length

	clusterName := randomString(filler, 20)
	if cfg.Clusters[clusterName] != nil {
		return "", false
	}
	cluster := clientcmdapi.NewCluster()
	cluster.Server = randomString(filler, 120) + ":6443" // Append a port to make it look like a valid server address
	filler.Fill(&cluster.CertificateAuthorityData)

	context := clientcmdapi.NewContext()
	context.AuthInfo = authInfoName
	context.Cluster = clusterName

	cfg.AuthInfos[authInfoName] = authInfo
	cfg.Clusters[clusterName] = cluster
	cfg.Contexts[contextName] = context

	return contextName, true
}
