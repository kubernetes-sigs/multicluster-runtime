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

package clusterinventoryapi

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBuilder(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Cluster Inventory API Provider Suite")
}

var testenvHub *envtest.Environment
var cfgHub *rest.Config

var testenvMember *envtest.Environment
var cfgMember *rest.Config

var _ = BeforeSuite(func() {
	runtime.Must(clusterinventoryv1alpha1.AddToScheme(scheme.Scheme))

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	assetDir := GinkgoT().TempDir()

	clusterProfileCRDPath := filepath.Join(assetDir, "multicluster.x-k8s.io_clusterprofiles.yaml")
	Expect(DownloadFile(
		clusterProfileCRDPath,
		"https://raw.githubusercontent.com/kubernetes-sigs/cluster-inventory-api/refs/heads/main/config/crd/bases/multicluster.x-k8s.io_clusterprofiles.yaml",
	)).NotTo(HaveOccurred())

	testenvHub = &envtest.Environment{
		ErrorIfCRDPathMissing: true,
		CRDDirectoryPaths:     []string{clusterProfileCRDPath},
	}
	testenvMember = &envtest.Environment{}

	var err error
	cfgHub, err = testenvHub.Start()
	Expect(err).NotTo(HaveOccurred())
	cfgMember, err = testenvMember.Start()
	Expect(err).NotTo(HaveOccurred())

	// Prevent the metrics listener being created
	metricsserver.DefaultBindAddress = "0"
})

var _ = AfterSuite(func() {
	if testenvHub != nil {
		Expect(testenvHub.Stop()).To(Succeed())
	}
	if testenvMember != nil {
		Expect(testenvMember.Stop()).To(Succeed())
	}

	// Put the DefaultBindAddress back
	metricsserver.DefaultBindAddress = ":8080"
})

func DownloadFile(filepath string, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
