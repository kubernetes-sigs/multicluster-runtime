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
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Provider Cluster Inventory API", Ordered, func() {
	ctx, cancel := context.WithCancel(context.Background())
	g, ctx := errgroup.WithContext(ctx)

	const consumerName = "hub"
	var localMgr manager.Manager
	var provider *Provider
	var mgr mcmanager.Manager

	var cliHub client.Client

	var cliMember client.Client
	var profileMember *clusterinventoryv1alpha1.ClusterProfile
	var sa1TokenMember string
	var sa2TokenMember string

	BeforeAll(func() {
		var err error
		cliHub, err = client.New(cfgHub, client.Options{})
		Expect(err).NotTo(HaveOccurred())

		cliMember, err = client.New(cfgMember, client.Options{})
		Expect(err).NotTo(HaveOccurred())

		By("Setting up the Local manager", func() {
			localMgr, err = manager.New(cfgHub, manager.Options{})
			Expect(err).NotTo(HaveOccurred())
		})

		By("Setting up the Provider", func() {
			var err error
			provider, err = New(localMgr, Options{
				ConsumerName: consumerName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(provider).NotTo(BeNil())
		})

		By("Setting up the cluster-aware manager, with the provider to lookup clusters", func() {
			var err error
			mgr, err = mcmanager.New(cfgHub, provider, manager.Options{})
			Expect(err).NotTo(HaveOccurred())
		})

		By("Setting up the controller feeding the animals", func() {
			err := mcbuilder.ControllerManagedBy(mgr).
				Named("fleet-configmap-controller").
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
						log.Info("Fed the animal", "configmap", cm.Name)
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
				err := localMgr.Start(ctx)
				return ignoreCanceled(err)
			})
			g.Go(func() error {
				err := provider.Run(ctx, mgr)
				return ignoreCanceled(err)
			})
			g.Go(func() error {
				err := mgr.Start(ctx)
				return ignoreCanceled(err)
			})
		})

		By("Setting up the ClusterProfile for member clusters", func() {
			profileMember = &clusterinventoryv1alpha1.ClusterProfile{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "member",
					Namespace: "default",
				},
				Spec: clusterinventoryv1alpha1.ClusterProfileSpec{
					DisplayName: "member",
					ClusterManager: clusterinventoryv1alpha1.ClusterManager{
						Name: "test",
					},
				},
			}
			Expect(cliHub.Create(ctx, profileMember)).To(Succeed())
			// Mock the control plane health condition
			profileMember.Status.Conditions = append(profileMember.Status.Conditions, metav1.Condition{
				Type:               clusterinventoryv1alpha1.ClusterConditionControlPlaneHealthy,
				Status:             metav1.ConditionTrue,
				Reason:             "Healthy",
				Message:            "Control plane is mocked as healthy",
				LastTransitionTime: metav1.Now(),
			})
			Expect(cliHub.Status().Update(ctx, profileMember)).To(Succeed())

			_, sa1TokenMember = mustCreateAdminSAAndToken(ctx, cliMember, "sa1", "default")
			_ = mustCreateOrUpdateKubeConfigSecretFromTokenSecret(
				ctx, cliHub, cfgMember,
				consumerName,
				*profileMember,
				sa1TokenMember,
			)
		})

	})

	BeforeAll(func() {
		runtime.Must(client.IgnoreAlreadyExists(cliMember.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "jungle"}})))
		runtime.Must(client.IgnoreAlreadyExists(cliMember.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "monkey", Labels: map[string]string{"type": "animal"}}})))
		runtime.Must(client.IgnoreAlreadyExists(cliMember.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tree", Labels: map[string]string{"type": "thing"}}})))
		runtime.Must(client.IgnoreAlreadyExists(cliMember.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tarzan", Labels: map[string]string{"type": "human"}}})))
	})

	It("runs the reconciler for existing objects", func(ctx context.Context) {
		Eventually(func() string {
			lion := &corev1.ConfigMap{}
			err := cliMember.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "monkey"}, lion)
			Expect(err).NotTo(HaveOccurred())
			return lion.Data["stomach"]
		}, "10s").Should(Equal("food"))
	})

	It("runs the reconciler for new objects", func(ctx context.Context) {
		By("Creating a new configmap", func() {
			err := cliMember.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "gorilla", Labels: map[string]string{"type": "animal"}}})
			Expect(err).NotTo(HaveOccurred())
		})

		Eventually(func() string {
			tiger := &corev1.ConfigMap{}
			err := cliMember.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "gorilla"}, tiger)
			Expect(err).NotTo(HaveOccurred())
			return tiger.Data["stomach"]
		}, "10s").Should(Equal("food"))
	})

	It("runs the reconciler for updated objects", func(ctx context.Context) {
		updated := &corev1.ConfigMap{}
		By("Emptying the gorilla's stomach", func() {
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				if err := cliMember.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "gorilla"}, updated); err != nil {
					return err
				}
				updated.Data = map[string]string{}
				return cliMember.Update(ctx, updated)
			})
			Expect(err).NotTo(HaveOccurred())
		})
		rv, err := strconv.ParseInt(updated.ResourceVersion, 10, 64)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func() int64 {
			elephant := &corev1.ConfigMap{}
			err := cliMember.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "gorilla"}, elephant)
			Expect(err).NotTo(HaveOccurred())
			rv, err := strconv.ParseInt(elephant.ResourceVersion, 10, 64)
			Expect(err).NotTo(HaveOccurred())
			return rv
		}, "10s").Should(BeNumerically(">=", rv))

		Eventually(func() string {
			elephant := &corev1.ConfigMap{}
			err := cliMember.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "gorilla"}, elephant)
			Expect(err).NotTo(HaveOccurred())
			return elephant.Data["stomach"]
		}, "10s").Should(Equal("food"))
	})

	It("queries one cluster via a multi-cluster index", func() {
		cl, err := mgr.GetCluster(ctx, "default/member")
		Expect(err).NotTo(HaveOccurred())

		cms := &corev1.ConfigMapList{}
		err = cl.GetCache().List(ctx, cms, client.MatchingFields{"type": "human"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cms.Items).To(HaveLen(1))
		Expect(cms.Items[0].Name).To(Equal("tarzan"))
		Expect(cms.Items[0].Namespace).To(Equal("jungle"))
	})

	It("queries all clusters via a multi-cluster index with a namespace", func() {
		cl, err := mgr.GetCluster(ctx, "default/member")
		Expect(err).NotTo(HaveOccurred())
		cms := &corev1.ConfigMapList{}
		err = cl.GetCache().List(ctx, cms, client.InNamespace("jungle"), client.MatchingFields{"type": "human"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cms.Items).To(HaveLen(1))
		Expect(cms.Items[0].Name).To(Equal("tarzan"))
		Expect(cms.Items[0].Namespace).To(Equal("jungle"))
	})

	It("re-engages the cluster when kubeconfig of the cluster profile changes", func(ctx context.Context) {
		By("Update the kubeconfig for the member ClusterProfile", func() {
			_, sa2TokenMember = mustCreateAdminSAAndToken(ctx, cliMember, "sa2", "default")
			_ = mustCreateOrUpdateKubeConfigSecretFromTokenSecret(
				ctx, cliHub, cfgMember,
				consumerName,
				*profileMember,
				sa2TokenMember,
			)
		})

		By("runs the reconciler for new objects(i.e. waiting for the reconciler to re-engage the cluster)", func() {
			time.Sleep(2 * time.Second) // Give some time for the reconciler to pick up the new kubeconfig
			jaguar := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "jungle",
					Name:      "jaguar",
					Labels:    map[string]string{"type": "animal"},
				},
			}
			Expect(cliMember.Create(ctx, jaguar)).NotTo(HaveOccurred())
			Eventually(func(g Gomega) string {
				err := cliMember.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "jaguar"}, jaguar)
				g.Expect(err).NotTo(HaveOccurred())
				return jaguar.Data["stomach"]
			}, "10s").Should(Equal("food"))
		})
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

func mustCreateAdminSAAndToken(ctx context.Context, cli client.Client, name, namespace string) (corev1.ServiceAccount, string) {
	sa := corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	Expect(cli.Create(ctx, &sa)).To(Succeed())

	tokenRequest := authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         []string{},
			ExpirationSeconds: ptr.To(int64(86400)), // 1 day
		},
	}
	Expect(cli.SubResource("token").Create(ctx, &sa, &tokenRequest)).NotTo(HaveOccurred())

	adminClusterRoleBinding := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name + "-admin",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      sa.Name,
				Namespace: sa.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
	}
	Expect(cli.Create(ctx, &adminClusterRoleBinding)).To(Succeed())

	return sa, tokenRequest.Status.Token
}

func mustCreateOrUpdateKubeConfigSecretFromTokenSecret(
	ctx context.Context,
	cli client.Client,
	cfg *rest.Config,
	consumerName string,
	clusterProfile clusterinventoryv1alpha1.ClusterProfile,
	token string,
) corev1.Secret {
	kubeconfigStr := fmt.Sprintf(`apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: |-
      %s
    server: %s
  name: cluster
contexts:
- context:
    cluster: cluster
    user: user
  name: cluster
current-context: cluster
kind: Config
users:
- name: user
  user:
    token: |-
      %s
`, base64.StdEncoding.EncodeToString(cfg.CAData), cfg.Host, token)

	kubeConfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-kubeconfig", clusterProfile.Name),
			Namespace: "default",
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, cli, kubeConfigSecret, func() error {
		kubeConfigSecret.Labels = map[string]string{
			labelKeyClusterProfile:           clusterProfile.Name,
			labelKeyClusterInventoryConsumer: consumerName,
		}
		kubeConfigSecret.StringData = map[string]string{
			dataKeyKubeConfig: kubeconfigStr,
		}
		return nil
	})
	Expect(err).NotTo(HaveOccurred())
	return *kubeConfigSecret
}
