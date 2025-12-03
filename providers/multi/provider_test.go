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
	"context"
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sync/errgroup"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/retry"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	nsprovider "sigs.k8s.io/multicluster-runtime/providers/namespace"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Provider Multi", Ordered, func() {
	ctx, cancel := context.WithCancel(context.Background())
	g, ctx := errgroup.WithContext(ctx)

	testTimeout := "10s"

	var provider *Provider
	var mgr mcmanager.Manager
	var cloud1client, cloud2client client.Client
	var cloud1cluster, cloud2cluster cluster.Cluster
	var cloud1provider, cloud2provider *nsprovider.Provider

	BeforeAll(func() {
		By("Setting up the first namespace provider", func() {
			var err error
			cloud1client, err = client.New(cloud1cfg, client.Options{})
			Expect(err).NotTo(HaveOccurred())

			cloud1cluster, err = cluster.New(cloud1cfg)
			Expect(err).NotTo(HaveOccurred())
			g.Go(func() error {
				return ignoreCanceled(cloud1cluster.Start(ctx))
			})

			cloud1provider = nsprovider.New(cloud1cluster)
		})

		By("Setting up the second namespace provider", func() {
			var err error
			cloud2client, err = client.New(cloud2cfg, client.Options{})
			Expect(err).NotTo(HaveOccurred())

			cloud2cluster, err = cluster.New(cloud2cfg)
			Expect(err).NotTo(HaveOccurred())
			g.Go(func() error {
				return ignoreCanceled(cloud2cluster.Start(ctx))
			})

			cloud2provider = nsprovider.New(cloud2cluster)
		})

		By("Setting up the provider and manager", func() {
			provider = New(Options{})

			var err error
			mgr, err = mcmanager.New(localCfg, provider, manager.Options{})
			Expect(err).NotTo(HaveOccurred())
		})

		By("Adding the first namespace provider before starting the manager", func() {
			_, ok := provider.GetProvider("cloud1")
			Expect(ok).To(BeFalse())

			err := provider.AddProvider("cloud1", cloud1provider)
			Expect(err).NotTo(HaveOccurred())

			returnedProvider, ok := provider.GetProvider("cloud1")
			Expect(ok).To(BeTrue())
			Expect(returnedProvider).To(Equal(cloud1provider))
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

		By("Filling the providers with data", func() {
			// cluster zoo exists in cloud1
			runtime.Must(client.IgnoreAlreadyExists(cloud1client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "zoo"}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud1client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "elephant", Labels: map[string]string{"type": "animal"}}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud1client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "lion", Labels: map[string]string{"type": "animal"}}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud1client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "keeper", Labels: map[string]string{"type": "human"}}})))

			// cluster jungle exists in cloud2
			runtime.Must(client.IgnoreAlreadyExists(cloud2client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "jungle"}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud2client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "monkey", Labels: map[string]string{"type": "animal"}}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud2client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tree", Labels: map[string]string{"type": "thing"}}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud2client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tarzan", Labels: map[string]string{"type": "human"}}})))

			// cluster island exists in both clouds
			runtime.Must(client.IgnoreAlreadyExists(cloud1client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "island"}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud1client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "bird", Labels: map[string]string{"type": "animal"}}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud1client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "stone", Labels: map[string]string{"type": "thing"}}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud1client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "crusoe", Labels: map[string]string{"type": "human"}}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud2client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "island"}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud2client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "bird", Labels: map[string]string{"type": "animal"}}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud2client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "stone", Labels: map[string]string{"type": "thing"}}})))
			runtime.Must(client.IgnoreAlreadyExists(cloud2client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "selkirk", Labels: map[string]string{"type": "human"}}})))
		})

		By("Starting the manager", func() {
			g.Go(func() error {
				return ignoreCanceled(mgr.Start(ctx))
			})
		})

		By("Adding an index before adding the last provider", func() {
			err := mgr.GetFieldIndexer().IndexField(ctx, &corev1.ConfigMap{}, "type", func(obj client.Object) []string {
				return []string{obj.GetLabels()["type"]}
			})
			Expect(err).NotTo(HaveOccurred())
		})

		By("Adding the second namespace provider after starting the manager", func() {
			err := provider.AddProvider("cloud2", cloud2provider)
			Expect(err).NotTo(HaveOccurred())

			providerNames := provider.ProviderNames()
			Expect(providerNames).To(ContainElements("cloud1", "cloud2"))
		})
	})

	It("runs the reconciler for existing objects", func(ctx context.Context) {
		Eventually(func(g Gomega) string {
			cl, err := mgr.GetCluster(ctx, "cloud1#zoo")
			g.Expect(err).NotTo(HaveOccurred())
			lion := &corev1.ConfigMap{}
			err = cl.GetClient().Get(ctx, client.ObjectKey{Namespace: "default", Name: "lion"}, lion)
			g.Expect(err).NotTo(HaveOccurred())
			return lion.Data["stomach"]
		}, testTimeout).Should(Equal("food"))
	})

	It("runs the reconciler for new objects", func(ctx context.Context) {
		By("Creating a new configmap", func() {
			err := cloud1client.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "tiger", Labels: map[string]string{"type": "animal"}}})
			Expect(err).NotTo(HaveOccurred())
		})

		Eventually(func(g Gomega) string {
			cl, err := mgr.GetCluster(ctx, "cloud1#zoo")
			g.Expect(err).NotTo(HaveOccurred())
			tiger := &corev1.ConfigMap{}
			err = cl.GetClient().Get(ctx, client.ObjectKey{Namespace: "default", Name: "tiger"}, tiger)
			g.Expect(err).NotTo(HaveOccurred())
			return tiger.Data["stomach"]
		}, testTimeout).Should(Equal("food"))
	})

	It("runs the reconciler for updated objects", func(ctx context.Context) {
		updated := &corev1.ConfigMap{}
		By("Emptying the elephant's stomach", func() {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := cloud1client.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "elephant"}, updated); err != nil {
					return err
				}
				updated.Data = map[string]string{}
				return cloud1client.Update(ctx, updated)
			})
			Expect(err).NotTo(HaveOccurred())
		})
		rv, err := strconv.ParseInt(updated.ResourceVersion, 10, 64)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) int64 {
			cl, err := mgr.GetCluster(ctx, "cloud1#zoo")
			g.Expect(err).NotTo(HaveOccurred())
			elephant := &corev1.ConfigMap{}
			err = cl.GetClient().Get(ctx, client.ObjectKey{Namespace: "default", Name: "elephant"}, elephant)
			g.Expect(err).NotTo(HaveOccurred())
			rv, err := strconv.ParseInt(elephant.ResourceVersion, 10, 64)
			g.Expect(err).NotTo(HaveOccurred())
			return rv
		}, testTimeout).Should(BeNumerically(">=", rv))

		Eventually(func(g Gomega) string {
			cl, err := mgr.GetCluster(ctx, "cloud1#zoo")
			g.Expect(err).NotTo(HaveOccurred())
			elephant := &corev1.ConfigMap{}
			err = cl.GetClient().Get(ctx, client.ObjectKey{Namespace: "default", Name: "elephant"}, elephant)
			g.Expect(err).NotTo(HaveOccurred())
			return elephant.Data["stomach"]
		}, testTimeout).Should(Equal("food"))
	})

	It("queries island on cloud1 via a multi-cluster index", func() {
		island, err := mgr.GetCluster(ctx, "cloud1#island")
		Expect(err).NotTo(HaveOccurred())
		cms := &corev1.ConfigMapList{}
		err = island.GetCache().List(ctx, cms, client.MatchingFields{"type": "human"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cms.Items).To(HaveLen(1))
		Expect(cms.Items[0].Name).To(Equal("crusoe"))
		Expect(cms.Items[0].Namespace).To(Equal("default"))
	})

	It("queries island on cloud2 via a multi-cluster index", func() {
		island, err := mgr.GetCluster(ctx, "cloud2#island")
		Expect(err).NotTo(HaveOccurred())
		cms := &corev1.ConfigMapList{}
		err = island.GetCache().List(ctx, cms, client.MatchingFields{"type": "human"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cms.Items).To(HaveLen(1))
		Expect(cms.Items[0].Name).To(Equal("selkirk"))
		Expect(cms.Items[0].Namespace).To(Equal("default"))
	})

	AfterAll(func() {
		By("Stopping the provider, cluster, manager, and controller", func() {
			cancel()
		})
		By("Waiting for the error group to finish", func() {
			err := g.Wait()
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
