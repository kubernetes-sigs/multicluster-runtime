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

package multi

import (
	"testing"

	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBuilder(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Multi Provider Suite")
}

// The operator runs in a local cluster and embeds two other providers
// for cloud providers. The cloud providers are simulated by using the
// namespace provider with two other clusters.

var localEnv *envtest.Environment
var localCfg *rest.Config

var cloud1 *envtest.Environment
var cloud1cfg *rest.Config

var cloud2 *envtest.Environment
var cloud2cfg *rest.Config

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	var err error

	localEnv = &envtest.Environment{}
	localCfg, err = localEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	cloud1 = &envtest.Environment{}
	cloud1cfg, err = cloud1.Start()
	Expect(err).NotTo(HaveOccurred())

	cloud2 = &envtest.Environment{}
	cloud2cfg, err = cloud2.Start()
	Expect(err).NotTo(HaveOccurred())

	// Prevent the metrics listener being created
	metricsserver.DefaultBindAddress = "0"
})

var _ = AfterSuite(func() {
	if localEnv != nil {
		Expect(localEnv.Stop()).To(Succeed())
	}

	if cloud1 != nil {
		Expect(cloud1.Stop()).To(Succeed())
	}

	if cloud2 != nil {
		Expect(cloud2.Stop()).To(Succeed())
	}

	// Put the DefaultBindAddress back
	metricsserver.DefaultBindAddress = ":8080"
})
