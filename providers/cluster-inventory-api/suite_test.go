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
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBuilder(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Cluster Inventory API Provider Suite")
}

var clusterProfileCRDPath string

var _ = BeforeSuite(func() {
	runtime.Must(clusterinventoryv1alpha1.AddToScheme(scheme.Scheme))

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	clusterInventoryAPIDir, err := moduleDir("sigs.k8s.io/cluster-inventory-api")
	Expect(err).NotTo(HaveOccurred())
	clusterProfileCRDPath = filepath.Join(clusterInventoryAPIDir, "config/crd/bases/multicluster.x-k8s.io_clusterprofiles.yaml")
	Expect(clusterProfileCRDPath).To(BeAnExistingFile())

	// Prevent the metrics listener being created
	metricsserver.DefaultBindAddress = "0"
})

var _ = AfterSuite(func() {
	// Put the DefaultBindAddress back
	metricsserver.DefaultBindAddress = ":8080"
})

func moduleDir(module string) (string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", module)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go list module directory for %s: %w: %s", module, err, strings.TrimSpace(string(output)))
	}
	dir := strings.TrimSpace(string(output))
	if dir == "" {
		return "", fmt.Errorf("go list module directory for %s returned an empty path", module)
	}
	return dir, nil
}
