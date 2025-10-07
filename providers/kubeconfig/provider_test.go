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
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/retry"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const kubeconfigSecretLabel = "kubeconfig.multicluster.io/secret"
const kubeconfigSecretNamespace = "testing"
const kubeconfigSecretKey = "config"

var _ = Describe("Provider Namespace", Ordered, func() {
	ctx, cancel := context.WithCancel(context.Background())
	wg := sync.WaitGroup{}

	var provider *Provider
	// var cl cluster.Cluster
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
			KubeconfigSecretLabel: kubeconfigSecretLabel,
			Namespace:             kubeconfigSecretNamespace,
			KubeconfigSecretKey:   kubeconfigSecretKey,
			RESTOptions: []func(cfg *rest.Config) error{
				func(cfg *rest.Config) error {
					cfg.QPS = 100
					cfg.Burst = 200
					return nil
				},
			},
			ClusterOptions: []cluster.Option{
				func(clusterOptions *cluster.Options) {
					clusterOptions.Scheme = scheme.Scheme
				},
			},
		})

		By("Creating a namespace in the local cluster", func() {
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: kubeconfigSecretNamespace,
				},
			}

			err = localCli.Create(ctx, namespace)
			Expect(err).NotTo(HaveOccurred())
		})

		By("Creating kubeconfig secrets in the local cluster", func() {
			err = createKubeconfigSecret(ctx, "zoo", zooCfg, localCli)
			Expect(err).NotTo(HaveOccurred())

			err = createKubeconfigSecret(ctx, "jungle", jungleCfg, localCli)
			Expect(err).NotTo(HaveOccurred())

			err = createKubeconfigSecret(ctx, "island", islandCfg, localCli)
			Expect(err).NotTo(HaveOccurred())
		})

		By("Setting up the cluster-aware manager, with the provider to lookup clusters", func() {
			var err error
			mgr, err = mcmanager.New(localCfg, provider, mcmanager.Options{
				Options: manager.Options{
					Metrics: metricsserver.Options{
						BindAddress: "0",
					},
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
		runtime.Must(client.IgnoreAlreadyExists(zooCli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "zoo"}})))
		runtime.Must(client.IgnoreAlreadyExists(zooCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "elephant", Labels: map[string]string{"type": "animal"}}})))
		runtime.Must(client.IgnoreAlreadyExists(zooCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "lion", Labels: map[string]string{"type": "animal"}}})))
		runtime.Must(client.IgnoreAlreadyExists(zooCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "keeper", Labels: map[string]string{"type": "human"}}})))

		runtime.Must(client.IgnoreAlreadyExists(jungleCli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "jungle"}})))
		runtime.Must(client.IgnoreAlreadyExists(jungleCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "monkey", Labels: map[string]string{"type": "animal"}}})))
		runtime.Must(client.IgnoreAlreadyExists(jungleCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tree", Labels: map[string]string{"type": "thing"}}})))
		runtime.Must(client.IgnoreAlreadyExists(jungleCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tarzan", Labels: map[string]string{"type": "human"}}})))

		runtime.Must(client.IgnoreAlreadyExists(islandCli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "island"}})))
		runtime.Must(client.IgnoreAlreadyExists(islandCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "bird", Labels: map[string]string{"type": "animal"}}})))
		runtime.Must(client.IgnoreAlreadyExists(islandCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "stone", Labels: map[string]string{"type": "thing"}}})))
		runtime.Must(client.IgnoreAlreadyExists(islandCli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "crusoe", Labels: map[string]string{"type": "human"}}})))
	})

	It("lists the clusters loaded from kubeconfig secrets", func() {
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

	It("reconciles objects when the cluster is updated in kubeconfig secret", func() {
		islandSecret := &corev1.Secret{}
		err := localCli.Get(ctx, client.ObjectKey{Name: "island", Namespace: kubeconfigSecretNamespace}, islandSecret)
		Expect(err).NotTo(HaveOccurred())

		kubeconfigData, err := createKubeConfig(jungleCfg)
		Expect(err).NotTo(HaveOccurred())

		islandSecret.Data = map[string][]byte{
			kubeconfigSecretKey: kubeconfigData,
		}
		err = localCli.Update(ctx, islandSecret)
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

	It("reconciles objects when kubeconfig secret is updated without changing the cluster", func() {
		jungleSecret := &corev1.Secret{}
		err := localCli.Get(ctx, client.ObjectKey{Name: "jungle", Namespace: kubeconfigSecretNamespace}, jungleSecret)
		Expect(err).NotTo(HaveOccurred())

		jungleSecret.ObjectMeta.Annotations = map[string]string{
			"location": "amazon",
		}
		err = localCli.Update(ctx, jungleSecret)
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

	It("removes a cluster from the provider when the kubeconfig secret is deleted", func() {
		err := localCli.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "island", Namespace: kubeconfigSecretNamespace}})
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

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func createKubeConfig(cfg *rest.Config) ([]byte, error) {
	name := "cluster"
	apiConfig := api.Config{
		Clusters: map[string]*api.Cluster{
			name: {
				Server:                   cfg.Host,
				CertificateAuthorityData: cfg.CAData,
			},
		},
		AuthInfos: map[string]*api.AuthInfo{
			name: {
				ClientCertificateData: cfg.CertData,
				ClientKeyData:         cfg.KeyData,
				Token:                 cfg.BearerToken,
			},
		},
		Contexts: map[string]*api.Context{
			name: {
				Cluster:  name,
				AuthInfo: name,
			},
		},
		CurrentContext: name,
	}
	kubeconfigData, err := clientcmd.Write(apiConfig)
	if err != nil {
		return nil, err
	}
	return kubeconfigData, nil
}

func createKubeconfigSecret(ctx context.Context, name string, cfg *rest.Config, cl client.Client) error {
	kubeconfigData, err := createKubeConfig(cfg)
	if err != nil {
		return err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: kubeconfigSecretNamespace,
			Labels: map[string]string{
				kubeconfigSecretLabel: "true",
			},
		},
	}
	secret.Data = map[string][]byte{
		kubeconfigSecretKey: kubeconfigData,
	}
	return cl.Create(ctx, secret)
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
					Expect(err).To(Or(BeNil(), MatchError(mcluster.ErrClusterNotFound)))
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
