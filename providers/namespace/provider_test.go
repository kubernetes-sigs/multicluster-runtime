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

package namespace

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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Provider Namespace", Ordered, func() {
	ctx, cancel := context.WithCancel(context.Background())
	g, ctx := errgroup.WithContext(ctx)

	var provider *Provider
	var cl cluster.Cluster
	var mgr mcmanager.Manager
	var cli client.Client

	BeforeAll(func() {
		var err error
		cli, err = client.New(cfg, client.Options{})
		Expect(err).NotTo(HaveOccurred())

		By("Setting up the provider against the host cluster", func() {
			var err error
			cl, err = cluster.New(cfg)
			Expect(err).NotTo(HaveOccurred())
			provider = New(cl)
		})

		By("Setting up the cluster-aware manager, with the provider to lookup clusters", func() {
			var err error
			mgr, err = mcmanager.New(cfg, provider, mcmanager.Options{})
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

			By("Adding an index to the provider clusters", func() {
				err := mgr.GetFieldIndexer().IndexField(ctx, &corev1.ConfigMap{}, "type", func(obj client.Object) []string {
					return []string{obj.GetLabels()["type"]}
				})
				Expect(err).NotTo(HaveOccurred())
			})
		})

		By("Starting the provider, cluster, manager, and controller", func() {
			g.Go(func() error {
				return ignoreCanceled(provider.Run(ctx, mgr))
			})
			g.Go(func() error {
				return ignoreCanceled(cl.Start(ctx))
			})
			g.Go(func() error {
				return ignoreCanceled(mgr.Start(ctx))
			})
		})
	})

	BeforeAll(func() {
		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "zoo"}})))
		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "elephant", Labels: map[string]string{"type": "animal"}}})))
		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "lion", Labels: map[string]string{"type": "animal"}}})))
		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "keeper", Labels: map[string]string{"type": "human"}}})))

		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "jungle"}})))
		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "monkey", Labels: map[string]string{"type": "animal"}}})))
		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tree", Labels: map[string]string{"type": "thing"}}})))
		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tarzan", Labels: map[string]string{"type": "human"}}})))

		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "island"}})))
		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "bird", Labels: map[string]string{"type": "animal"}}})))
		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "stone", Labels: map[string]string{"type": "thing"}}})))
		runtime.Must(client.IgnoreAlreadyExists(cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "island", Name: "crusoe", Labels: map[string]string{"type": "human"}}})))
	})

	It("runs the reconciler for existing objects", func(ctx context.Context) {
		Eventually(func() string {
			lion := &corev1.ConfigMap{}
			err := cli.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "lion"}, lion)
			Expect(err).NotTo(HaveOccurred())
			return lion.Data["stomach"]
		}, "10s").Should(Equal("food"))
	})

	It("runs the reconciler for new objects", func(ctx context.Context) {
		By("Creating a new configmap", func() {
			err := cli.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "zoo", Name: "tiger", Labels: map[string]string{"type": "animal"}}})
			Expect(err).NotTo(HaveOccurred())
		})

		Eventually(func() string {
			tiger := &corev1.ConfigMap{}
			err := cli.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "tiger"}, tiger)
			Expect(err).NotTo(HaveOccurred())
			return tiger.Data["stomach"]
		}, "10s").Should(Equal("food"))
	})

	It("runs the reconciler for updated objects", func(ctx context.Context) {
		updated := &corev1.ConfigMap{}
		By("Emptying the elephant's stomach", func() {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := cli.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "elephant"}, updated); err != nil {
					return err
				}
				updated.Data = map[string]string{}
				return cli.Update(ctx, updated)
			})
			Expect(err).NotTo(HaveOccurred())
		})
		rv, err := strconv.ParseInt(updated.ResourceVersion, 10, 64)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() int64 {
			elephant := &corev1.ConfigMap{}
			err := cli.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "elephant"}, elephant)
			Expect(err).NotTo(HaveOccurred())
			rv, err := strconv.ParseInt(elephant.ResourceVersion, 10, 64)
			Expect(err).NotTo(HaveOccurred())
			return rv
		}, "10s").Should(BeNumerically(">=", rv))

		Eventually(func() string {
			elephant := &corev1.ConfigMap{}
			err := cli.Get(ctx, client.ObjectKey{Namespace: "zoo", Name: "elephant"}, elephant)
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
		Expect(cms.Items[0].Namespace).To(Equal("default"))
	})

	It("queries one cluster via a multi-cluster index with a namespace", func() {
		island, err := mgr.GetCluster(ctx, "island")
		Expect(err).NotTo(HaveOccurred())

		cms := &corev1.ConfigMapList{}
		err = island.GetCache().List(ctx, cms, client.InNamespace("default"), client.MatchingFields{"type": "human"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cms.Items).To(HaveLen(1))
		Expect(cms.Items[0].Name).To(Equal("crusoe"))
		Expect(cms.Items[0].Namespace).To(Equal("default"))
	})

	It("queries all clusters via a multi-cluster index", func() {
		cms := &corev1.ConfigMapList{}
		err := cl.GetCache().List(ctx, cms, client.MatchingFields{"type": "human"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cms.Items).To(HaveLen(3))
		names := sets.NewString()
		for _, cm := range cms.Items {
			names.Insert(cm.Name)
		}
		Expect(names).To(Equal(sets.NewString("keeper", "tarzan", "crusoe")))
	})

	It("queries all clusters via a multi-cluster index with a namespace", func() {
		cms := &corev1.ConfigMapList{}
		err := cl.GetCache().List(ctx, cms, client.InNamespace("island"), client.MatchingFields{"type": "human"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cms.Items).To(HaveLen(1))
		Expect(cms.Items[0].Name).To(Equal("crusoe"))
		Expect(cms.Items[0].Namespace).To(Equal("island"))
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
