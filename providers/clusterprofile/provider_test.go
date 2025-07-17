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

package clusterprofile

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	clusterinventory "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/credentials"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/client-go/util/retry"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const clusterInventoryNamespace = "testing"

var _ = Describe("Provider Namespace", Ordered, func() {
	ctx, cancel := context.WithCancel(context.Background())
	wg := sync.WaitGroup{}

	var provider *Provider
	var mgr mcmanager.Manager
	var localCli client.Client
	var zooCli client.Client
	var jungleCli client.Client
	var islandCli client.Client

	BeforeAll(func() {
		var err error
		localCli, err = client.New(localCfg, client.Options{})
		Expect(err).NotTo(HaveOccurred())
		zooCli, err = client.New(zooCfg, client.Options{})
		Expect(err).NotTo(HaveOccurred())
		jungleCli, err = client.New(jungleCfg, client.Options{})
		Expect(err).NotTo(HaveOccurred())
		islandCli, err = client.New(islandCfg, client.Options{})
		Expect(err).NotTo(HaveOccurred())

		provider = New(Options{
			Namespace: clusterInventoryNamespace,
			CredentialsProvider: credentials.New([]credentials.Provider{
				{
					Name: "foobar",
					ExecConfig: &clientcmdapi.ExecConfig{
						Command:    "test-command-1",
						Args:       []string{"arg1", "arg2"},
						APIVersion: "client.authentication.k8s.io/v1beta1",
					},
				},
			}),
		})

		By("Creating a namespace in the local cluster", func() {
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: clusterInventoryNamespace,
				},
			}

			err = localCli.Create(ctx, namespace)
			Expect(err).NotTo(HaveOccurred())
		})

		By("Creating cluster profiles in the local cluster", func() {
			err = createClusterProfile(ctx, "zoo", zooCfg, localCli)
			Expect(err).NotTo(HaveOccurred())

			err = createClusterProfile(ctx, "jungle", jungleCfg, localCli)
			Expect(err).NotTo(HaveOccurred())

			err = createClusterProfile(ctx, "island", islandCfg, localCli)
			Expect(err).NotTo(HaveOccurred())
		})

		By("Setting up the cluster-aware manager, with the provider to lookup clusters", func() {
			var err error
			mgr, err = mcmanager.New(localCfg, provider, mcmanager.Options{
				Metrics: metricsserver.Options{
					BindAddress: "0",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		By("Setting up the provider with the manager", func() {
			err := provider.SetupWithManager(ctx, mgr)
			Expect(err).NotTo(HaveOccurred())
		})

		By("Setting up the controller feeding the animals", func() {
			err := mcbuilder.ControllerManagedBy(mgr).
				Named("fleet-ns-configmap-controller").
				For(&corev1.ConfigMap{}).
				Complete(mcreconcile.Func(
					func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
						log := log.FromContext(ctx).WithValues("request", req.String())
						log.Info("Reconciling ConfigMap")

						cl, err := mgr.GetCluster(ctx, req.ClusterName)
						if err != nil {
							return reconcile.Result{}, fmt.Errorf("failed to get cluster: %w", err)
						}

						// Feed the animal.
						cm := &corev1.ConfigMap{}
						if err := cl.GetClient().Get(ctx, req.NamespacedName, cm); err != nil {
							if apierrors.IsNotFound(err) {
								return reconcile.Result{}, nil
							}
							return reconcile.Result{}, fmt.Errorf("failed to get configmap: %w", err)
						}
						if cm.GetLabels()["type"] != "animal" {
							return reconcile.Result{}, nil
						}

						cm.Data = map[string]string{"stomach": "food"}
						if err := cl.GetClient().Update(ctx, cm); err != nil {
							return reconcile.Result{}, fmt.Errorf("failed to update configmap: %w", err)
						}

						return ctrl.Result{}, nil
					},
				))
			Expect(err).NotTo(HaveOccurred())
		})

		By("Adding an index to the provider clusters", func() {
			err := mgr.GetFieldIndexer().IndexField(ctx, &corev1.ConfigMap{}, "type", func(obj client.Object) []string {
				return []string{obj.GetLabels()["type"]}
			})
			Expect(err).NotTo(HaveOccurred())
		})

		By("Starting the provider, cluster, manager, and controller", func() {
			wg.Add(1)
			go func() {
				err := ignoreCanceled(mgr.Start(ctx))
				Expect(err).NotTo(HaveOccurred())
				wg.Done()
			}()
		})

	})

	BeforeAll(func() {
		utilruntime.Must(client.IgnoreAlreadyExists(zooCli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "zoo"}})))
		utilruntime.Must(client.IgnoreAlreadyExists(zooCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "elephant", Labels: map[string]string{"type": "animal"}}})))
		utilruntime.Must(client.IgnoreAlreadyExists(zooCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "lion", Labels: map[string]string{"type": "animal"}}})))
		utilruntime.Must(client.IgnoreAlreadyExists(zooCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "keeper", Labels: map[string]string{"type": "human"}}})))

		utilruntime.Must(client.IgnoreAlreadyExists(jungleCli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "jungle"}})))
		utilruntime.Must(client.IgnoreAlreadyExists(jungleCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "monkey", Labels: map[string]string{"type": "animal"}}})))
		utilruntime.Must(client.IgnoreAlreadyExists(jungleCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tree", Labels: map[string]string{"type": "thing"}}})))
		utilruntime.Must(client.IgnoreAlreadyExists(jungleCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tarzan", Labels: map[string]string{"type": "human"}}})))

		utilruntime.Must(client.IgnoreAlreadyExists(islandCli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "island"}})))
		utilruntime.Must(client.IgnoreAlreadyExists(islandCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "bird", Labels: map[string]string{"type": "animal"}}})))
		utilruntime.Must(client.IgnoreAlreadyExists(islandCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "stone", Labels: map[string]string{"type": "thing"}}})))
		utilruntime.Must(client.IgnoreAlreadyExists(islandCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "crusoe", Labels: map[string]string{"type": "human"}}})))
	})

	It("lists the clusters loaded from cluster profiles", func() {
		Eventually(provider.ListClusters, "10s").Should(HaveLen(3))
	})

	It("runs the reconciler for existing objects", func(ctx context.Context) {
		Eventually(func() string {
			lion := &corev1.ConfigMap{}
			err := zooCli.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "lion"}, lion)
			Expect(err).NotTo(HaveOccurred())
			return lion.Data["stomach"]
		}, "10s").Should(Equal("food"))
	})

	It("runs the reconciler for new objects", func(ctx context.Context) {
		By("Creating a new configmap", func() {
			err := zooCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "tiger", Labels: map[string]string{"type": "animal"}}})
			Expect(err).NotTo(HaveOccurred())
		})

		Eventually(func() string {
			tiger := &corev1.ConfigMap{}
			err := zooCli.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "tiger"}, tiger)
			Expect(err).NotTo(HaveOccurred())
			return tiger.Data["stomach"]
		}, "10s").Should(Equal("food"))
	})

	It("runs the reconciler for updated objects", func(ctx context.Context) {
		updated := &corev1.ConfigMap{}
		By("Emptying the elephant's stomach", func() {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := zooCli.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "elephant"}, updated); err != nil {
					return err
				}
				updated.Data = map[string]string{}
				return zooCli.Update(ctx, updated)
			})
			Expect(err).NotTo(HaveOccurred())
		})
		rv, err := strconv.ParseInt(updated.ResourceVersion, 10, 64)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() int64 {
			elephant := &corev1.ConfigMap{}
			err := zooCli.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "elephant"}, elephant)
			Expect(err).NotTo(HaveOccurred())
			rv, err := strconv.ParseInt(elephant.ResourceVersion, 10, 64)
			Expect(err).NotTo(HaveOccurred())
			return rv
		}, "10s").Should(BeNumerically(">=", rv))

		Eventually(func() string {
			elephant := &corev1.ConfigMap{}
			err := zooCli.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "elephant"}, elephant)
			Expect(err).NotTo(HaveOccurred())
			return elephant.Data["stomach"]
		}, "10s").Should(Equal("food"))
	})

	It("queries one cluster via a multi-cluster index", func() {
		island, err := mgr.GetCluster(ctx, "island")
		Expect(err).NotTo(HaveOccurred())

		cms := &corev1.ConfigMapList{}
		err = island.GetCache().List(ctx, cms, client.MatchingFields{"type": "human"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cms.Items).To(HaveLen(1))
		Expect(cms.Items[0].Name).To(Equal("crusoe"))
		Expect(cms.Items[0].Namespace).To(Equal("island"))
	})

	It("reconciles objects when the cluster is updated in cluster profile", func() {
		islandClusterProfile := &clusterinventory.ClusterProfile{}
		err := localCli.Get(ctx, client.ObjectKey{Name: "island", Namespace: clusterInventoryNamespace}, islandClusterProfile)
		Expect(err).NotTo(HaveOccurred())

		// Update the cluster profile to point to jungle configuration
		islandClusterProfile.Status.CredentialProviders = []clusterinventory.CredentialProvider{
			{
				Name: "foobar",
				Cluster: clientcmdv1.Cluster{
					Server:                   jungleCfg.Host,
					CertificateAuthorityData: jungleCfg.CAData,
				},
			},
		}
		err = localCli.Status().Update(ctx, islandClusterProfile)
		Expect(err).NotTo(HaveOccurred())

		err = jungleCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "dog", Labels: map[string]string{"type": "animal"}}})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() string {
			dog := &corev1.ConfigMap{}
			err := jungleCli.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "dog"}, dog)
			Expect(err).NotTo(HaveOccurred())
			return dog.Data["stomach"]
		}, "10s").Should(Equal("food"))
	})

	It("reconciles objects when cluster profile is updated without changing the cluster", func() {
		jungleClusterProfile := &clusterinventory.ClusterProfile{}
		err := localCli.Get(ctx, client.ObjectKey{Name: "jungle", Namespace: clusterInventoryNamespace}, jungleClusterProfile)
		Expect(err).NotTo(HaveOccurred())

		jungleClusterProfile.ObjectMeta.Annotations = map[string]string{
			"location": "amazon",
		}
		err = localCli.Update(ctx, jungleClusterProfile)
		Expect(err).NotTo(HaveOccurred())

		err = jungleCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "leopard", Labels: map[string]string{"type": "animal"}}})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() string {
			leopard := &corev1.ConfigMap{}
			err := jungleCli.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "leopard"}, leopard)
			Expect(err).NotTo(HaveOccurred())
			return leopard.Data["stomach"]
		}, "10s").Should(Equal("food"))
	})

	It("removes a cluster from the provider when the cluster profile is deleted", func() {
		err := localCli.Delete(ctx, &clusterinventory.ClusterProfile{ObjectMeta: metav1.ObjectMeta{Name: "island", Namespace: clusterInventoryNamespace}})
		Expect(err).NotTo(HaveOccurred())
		Eventually(provider.ListClusters, "10s").Should(HaveLen(2))
	})

	AfterAll(func() {
		By("Stopping the provider, cluster, manager, and controller", func() {
			cancel()
			wg.Wait()
		})
	})
})

var _ = Describe("Provider race condition", func() {
	It("should handle concurrent operations without issues", func() {
		p := New(Options{})

		// Pre-populate with some clusters to make the test meaningful
		numClusters := 20
		for i := 0; i < numClusters; i++ {
			clusterName := fmt.Sprintf("cluster-%d", i)
			p.clusters[clusterName] = activeCluster{
				Cluster: &mockCluster{},
				Cancel:  func() {},
			}
		}

		var wg sync.WaitGroup
		numGoroutines := 40
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(i int) {
				defer GinkgoRecover()
				defer wg.Done()

				// Mix of operations to stress the provider
				switch i % 4 {
				case 0:
					// Concurrently index a field. This will read the cluster list.
					err := p.IndexField(context.Background(), &corev1.Pod{}, "spec.nodeName", func(rawObj client.Object) []string {
						return nil
					})
					Expect(err).NotTo(HaveOccurred())
				case 1:
					// Concurrently get a cluster.
					_, err := p.Get(context.Background(), "cluster-1")
					Expect(err).To(Or(BeNil(), MatchError("cluster cluster-1 not found")))
				case 2:
					// Concurrently list clusters.
					p.ListClusters()
				case 3:
					// Concurrently delete a cluster. This will modify the cluster map.
					clusterToRemove := fmt.Sprintf("cluster-%d", i/4)
					p.removeCluster(clusterToRemove)
				}
			}(i)
		}

		wg.Wait()
	})
})

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func createClusterProfile(ctx context.Context, name string, cfg *rest.Config, cl client.Client) error {
	cp := &clusterinventory.ClusterProfile{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: clusterInventoryNamespace,
		},
		Status: clusterinventory.ClusterProfileStatus{
			CredentialProviders: []clusterinventory.CredentialProvider{
				{
					Name: "foobar",
					Cluster: clientcmdv1.Cluster{
						Server:                   cfg.Host,
						CertificateAuthorityData: cfg.CAData,
					},
				},
			},
		},
	}

	return cl.Create(ctx, cp)
}

// mockCluster is a mock implementation of cluster.Cluster for testing.
type mockCluster struct {
	cluster.Cluster
}

func (c *mockCluster) GetFieldIndexer() client.FieldIndexer {
	return &mockFieldIndexer{}
}

type mockFieldIndexer struct{}

func (f *mockFieldIndexer) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	// Simulate work to increase chance of race
	time.Sleep(time.Millisecond)
	return nil
}
