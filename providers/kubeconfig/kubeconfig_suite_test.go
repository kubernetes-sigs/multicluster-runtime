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

package kubeconfig

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
	RunSpecs(t, "Kubeconfig Provider Suite")
}

var localEnv *envtest.Environment
var localCfg *rest.Config

var zooEnv *envtest.Environment
var zooCfg *rest.Config

var jungleEnv *envtest.Environment
var jungleCfg *rest.Config

var islandEnv *envtest.Environment
var islandCfg *rest.Config

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	var err error

	// 'local' cluster runs the manager
	localEnv = &envtest.Environment{}
	localCfg, err = localEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	// startup 'remote' clusters
	zooEnv = &envtest.Environment{}
	zooCfg, err = zooEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	jungleEnv = &envtest.Environment{}
	jungleCfg, err = jungleEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	islandEnv = &envtest.Environment{}
	islandCfg, err = islandEnv.Start()
	Expect(err).NotTo(HaveOccurred())

	// Prevent the metrics listener being created
	metricsserver.DefaultBindAddress = "0"
})

var _ = AfterSuite(func() {
	if localEnv != nil {
		Expect(localEnv.Stop()).To(Succeed())
	}

	if zooEnv != nil {
		Expect(zooEnv.Stop()).To(Succeed())
	}

	if jungleEnv != nil {
		Expect(jungleEnv.Stop()).To(Succeed())
	}

	if islandEnv != nil {
		Expect(islandEnv.Stop()).To(Succeed())
	}

	// Put the DefaultBindAddress back
	metricsserver.DefaultBindAddress = ":8080"
})
