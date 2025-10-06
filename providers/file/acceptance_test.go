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
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"sigs.k8s.io/controller-runtime/pkg/envtest"

	mcacceptance "sigs.k8s.io/multicluster-runtime/pkg/acceptance"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Provider File Acceptance", Ordered, func() {
	var discoverDir string
	var provider multicluster.Provider
	var manager mcmanager.Manager
	var generateCluster mcacceptance.ClusterGenerator

	BeforeAll(func() {
		discoverDir = GinkgoT().TempDir()

		By("Creating a temporary directory for kubeconfig files", func() {
			err := os.MkdirAll(discoverDir, 0755)
			Expect(err).NotTo(HaveOccurred(), "Failed to create kubeconfig file")
		})

		By("Creating a new provider", func() {
			var err error
			provider, err = New(Options{
				KubeconfigDirs: []string{discoverDir},
			})
			Expect(err).NotTo(HaveOccurred())
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

				name := mcacceptance.RandomClusterName()
				kubeconfigPath := filepath.Join(discoverDir, name+".kubeconfig")
				kubeconfig := restToKubeconfig(cfg)
				if err := clientcmd.WriteToFile(*kubeconfig, kubeconfigPath); err != nil {
					return "", nil, fmt.Errorf("failed to write kubeconfig file: %w", err)
				}

				go func() {
					<-ctx.Done()
					errHandler(os.Remove(kubeconfigPath))
					errHandler(testenv.Stop())
				}()
				return kubeconfigPath + "+default-context", cfg, nil
			}
		})
	})

	It("Should run the acceptance tests", func() {
		mcacceptance.Provider(GinkgoTB(), generateCluster, manager)
	})
})

func restToKubeconfig(cfg *rest.Config) *api.Config {
	cluster := api.NewCluster()

	cluster.Server = cfg.Host
	cluster.CertificateAuthorityData = cfg.TLSClientConfig.CAData
	cluster.InsecureSkipTLSVerify = cfg.TLSClientConfig.Insecure

	authInfo := api.NewAuthInfo()
	authInfo.ClientCertificateData = cfg.TLSClientConfig.CertData
	authInfo.ClientKeyData = cfg.TLSClientConfig.KeyData
	authInfo.Token = cfg.BearerToken
	authInfo.Username = cfg.Username
	authInfo.Password = cfg.Password
	authInfo.AuthProvider = cfg.AuthProvider
	authInfo.Exec = cfg.ExecProvider

	context := api.NewContext()
	context.Cluster = "default-cluster"
	context.AuthInfo = "default-user"
	context.Namespace = "default"

	config := api.NewConfig()
	config.Clusters["default-cluster"] = cluster
	config.AuthInfos["default-user"] = authInfo
	config.Contexts["default-context"] = context
	config.CurrentContext = "default-context"

	return config
}
