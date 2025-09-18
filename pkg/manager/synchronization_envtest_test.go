package manager_test

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/providers/namespace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Synchronization engine envtest (namespace provider)", func() {
	It("distributes leases and reconciles per-namespace", func(ctx SpecContext) {
		// Build a direct client (no cache) for setup/reads
		sch := runtime.NewScheme()
		Expect(corev1.AddToScheme(sch)).To(Succeed())
		Expect(coordinationv1.AddToScheme(sch)).To(Succeed())
		directCli, err := client.New(cfg, client.Options{Scheme: sch})
		Expect(err).NotTo(HaveOccurred())

		// Ensure kube-system exists for fencing leases
		{
			var ks corev1.Namespace
			err := directCli.Get(ctx, client.ObjectKey{Name: "kube-system"}, &ks)
			if apierrors.IsNotFound(err) {
				Expect(directCli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}})).To(Succeed())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		}

		// Create N namespaces to act as clusters
		nsNames := []string{"zoo", "jungle", "island"}
		for _, n := range nsNames {
			Expect(directCli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n}})).To(Succeed())
		}

		// Provider: namespaces as clusters (uses its own cache via cluster)
		host, err := cluster.New(cfg)
		Expect(err).NotTo(HaveOccurred())
		prov := namespace.New(host)

		// Build mc manager with short timings
		m, err := mcmanager.New(cfg, prov, manager.Options{},
			mcmanager.WithShardLease("kube-system", "mcr-shard"),
			mcmanager.WithPerClusterLease(true),
			mcmanager.WithLeaseTimings(6*time.Second, 2*time.Second, 100*time.Millisecond),
			mcmanager.WithSynchronizationIntervals(200*time.Millisecond, 1*time.Second),
		)
		Expect(err).NotTo(HaveOccurred())

		// Add a trivial runnable to exercise engagement
		r := &noopRunnable{}
		Expect(m.Add(r)).To(Succeed())

		cctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Start manager and provider
		go func() { _ = prov.Run(cctx, m) }()
		go func() { _ = host.Start(cctx) }()
		go func() { _ = m.Start(cctx) }()

		// Eventually expect three leases with holders set (read via direct client)
		Eventually(func(g Gomega) {
			for _, n := range nsNames {
				var ls coordinationv1.Lease
				key := client.ObjectKey{Namespace: "kube-system", Name: fmt.Sprintf("mcr-shard-%s", n)}
				g.Expect(directCli.Get(ctx, key, &ls)).To(Succeed())
				g.Expect(ls.Spec.HolderIdentity).NotTo(BeNil())
				g.Expect(*ls.Spec.HolderIdentity).NotTo(BeEmpty())
			}
		}).WithTimeout(20 * time.Second).WithPolling(200 * time.Millisecond).Should(Succeed())
	})
})

type noopRunnable struct{}

func (n *noopRunnable) Start(ctx context.Context) error                                   { <-ctx.Done(); return ctx.Err() }
func (n *noopRunnable) Engage(ctx context.Context, name string, cl cluster.Cluster) error { return nil }
