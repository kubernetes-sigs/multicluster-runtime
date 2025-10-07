package manager_test

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

func TestManager(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Manager Envtest Suite")
}

var testEnv *envtest.Environment
var cfg *rest.Config

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))
	testEnv = &envtest.Environment{}
	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	// Prevent the metrics listener being created
	metricsserver.DefaultBindAddress = "0"
})

var _ = AfterSuite(func() {
	if testEnv != nil {
		Expect(testEnv.Stop()).To(Succeed())
	}
	// Put the DefaultBindAddress back
	metricsserver.DefaultBindAddress = ":8080"
})
