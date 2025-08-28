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
	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/credentials"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/providers/cluster-inventory-api/kubeconfigstrategy"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Provider Cluster Inventory API", Ordered, func() {
	var ctx context.Context
	var cancel context.CancelFunc
	var g *errgroup.Group

	var testenvHub *envtest.Environment
	var cfgHub *rest.Config
	var cliHub client.Client

	var testenvMember *envtest.Environment
	var cfgMember *rest.Config
	var cliMember client.Client

	var provider *Provider
	var mgr mcmanager.Manager
	var profileMember *clusterinventoryv1alpha1.ClusterProfile

	//
	// Common Behaviors
	//
	createClusters := func() {
		var err error
		By("Creating the hub cluster", func() {
			testenvHub = &envtest.Environment{
				ErrorIfCRDPathMissing: true,
				CRDDirectoryPaths:     []string{clusterProfileCRDPath},
			}
			cfgHub, err = testenvHub.Start()
			Expect(err).NotTo(HaveOccurred())
			cliHub, err = client.New(cfgHub, client.Options{})
			Expect(err).NotTo(HaveOccurred())
		})
		By("Creating the member cluster", func() {
			testenvMember = &envtest.Environment{}
			cfgMember, err = testenvMember.Start()
			Expect(err).NotTo(HaveOccurred())
			cliMember, err = client.New(cfgMember, client.Options{})
			Expect(err).NotTo(HaveOccurred())
		})
	}
	shutDownClusters := func() {
		By("Stopping the hub cluster environment", func() {
			Expect(testenvHub.Stop()).To(Succeed())
		})
		By("Stopping the member cluster environment", func() {
			Expect(testenvMember.Stop()).To(Succeed())
		})
	}
	setupAndStartControllers := func() {
		By("Setting up the cluster-aware manager, with the provider to lookup clusters", func() {
			var err error
			mgr, err = mcmanager.New(cfgHub, provider, mcmanager.Options{
				Controller: config.Controller{
					SkipNameValidation: ptr.To(true),
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		By("Setting up the provider controller", func() {
			err := provider.SetupWithManager(mgr)
			Expect(err).NotTo(HaveOccurred())
		})

		By("Setting up the controller feeding the animals", func() {
			err := mcbuilder.ControllerManagedBy(mgr).
				Named("fleet-configmap-controller").
				WithOptions(mccontroller.Options{
					SkipNameValidation: ptr.To(true),
				}).
				For(&corev1.ConfigMap{}).
				Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
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
				}))
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
				err := mgr.Start(ctx)
				return ignoreCanceled(err)
			})
		})
	}
	createObjects := func() {
		By("Creating the namespace and configmaps", func() {
			Expect(
				client.IgnoreAlreadyExists(cliMember.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "jungle"}})),
			).To(Succeed())
			Expect(
				client.IgnoreAlreadyExists(cliMember.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "monkey", Labels: map[string]string{"type": "animal"}}})),
			).To(Succeed())
			Expect(
				client.IgnoreAlreadyExists(cliMember.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tree", Labels: map[string]string{"type": "thing"}}})),
			).To(Succeed())
			Expect(
				client.IgnoreAlreadyExists(cliMember.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "tarzan", Labels: map[string]string{"type": "human"}}})),
			).To(Succeed())
		})
	}
	assertBasicControllerBehavior := func() {
		It("runs the reconciler for existing objects", func(ctx context.Context) {
			Eventually(func() string {
				monkey := &corev1.ConfigMap{}
				err := cliMember.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "monkey"}, monkey)
				Expect(err).NotTo(HaveOccurred())
				return monkey.Data["stomach"]
			}, "10s").Should(Equal("food"))
		})

		It("runs the reconciler for new objects", func(ctx context.Context) {
			By("Creating a new configmap", func() {
				err := cliMember.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "jungle", Name: "gorilla", Labels: map[string]string{"type": "animal"}}})
				Expect(err).NotTo(HaveOccurred())
			})

			Eventually(func() string {
				gorilla := &corev1.ConfigMap{}
				err := cliMember.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "gorilla"}, gorilla)
				Expect(err).NotTo(HaveOccurred())
				return gorilla.Data["stomach"]
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
				gorilla := &corev1.ConfigMap{}
				err := cliMember.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "gorilla"}, gorilla)
				Expect(err).NotTo(HaveOccurred())
				rv, err := strconv.ParseInt(gorilla.ResourceVersion, 10, 64)
				Expect(err).NotTo(HaveOccurred())
				return rv
			}, "10s").Should(BeNumerically(">=", rv))

			Eventually(func() string {
				gorilla := &corev1.ConfigMap{}
				err := cliMember.Get(ctx, client.ObjectKey{Namespace: "jungle", Name: "gorilla"}, gorilla)
				Expect(err).NotTo(HaveOccurred())
				return gorilla.Data["stomach"]
			}, "10s").Should(Equal("food"))
		})
	}
	assertClusterIndexBehavior := func() {
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
	}

	Context("With Secret-based kubeconfig strategy", Ordered, func() {
		const consumerName = "hub"
		var sa1TokenMember string
		var sa2TokenMember string

		BeforeAll(func() {
			ctx, cancel = context.WithCancel(context.Background())
			g, _ = errgroup.WithContext(ctx)

			createClusters()

			By("Setting up the Provider", func() {
				var err error
				provider, err = New(Options{
					KubeconfigStrategyOption: kubeconfigstrategy.Option{
						Secret: &kubeconfigstrategy.SecretStrategyOption{
							ConsumerName: consumerName,
						},
					},
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(provider).NotTo(BeNil())
			})

			setupAndStartControllers()

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

				sa1TokenMember = mustCreateAdminSAAndToken(ctx, cliMember, "sa1", "default")
				mustCreateOrUpdateKubeConfigSecretFromTokenSecret(
					ctx, cliHub, cfgMember,
					consumerName,
					*profileMember,
					sa1TokenMember,
				)
			})

			createObjects()
		})

		assertBasicControllerBehavior()
		assertClusterIndexBehavior()

		It("re-engages the cluster when kubeconfig of the cluster profile changes", func(ctx context.Context) {
			By("Update the kubeconfig for the member ClusterProfile", func() {
				sa2TokenMember = mustCreateAdminSAAndToken(ctx, cliMember, "sa2", "default")
				mustCreateOrUpdateKubeConfigSecretFromTokenSecret(
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
			shutDownClusters()
		})
	})

	Context("With Credential-based kubeconfig strategy", Ordered, func() {
		var cancel context.CancelFunc
		var g *errgroup.Group
		credentialProviderName := "test"

		BeforeAll(func() {
			ctx, cancel = context.WithCancel(context.Background())
			g, _ = errgroup.WithContext(ctx)

			createClusters()

			By("Setting up the Provider", func() {
				sa1TokenMember := mustCreateAdminSAAndToken(ctx, cliMember, "sa1", "default")
				execPluginOutput := fmt.Sprintf(`{
					"apiVersion": "client.authentication.k8s.io/v1beta1",
					"kind": "ExecCredential",
					"status": {
						"token": "%s"
					}
				}`, sa1TokenMember)

				var err error
				provider, err = New(Options{
					KubeconfigStrategyOption: kubeconfigstrategy.Option{
						CredentialsProvider: &kubeconfigstrategy.CredentialsProviderOption{
							Provider: credentials.New([]credentials.Provider{{
								Name: credentialProviderName,
								ExecConfig: &clientcmdapi.ExecConfig{
									APIVersion: "client.authentication.k8s.io/v1beta1",
									Command:    "sh",
									Args:       []string{"-c", fmt.Sprintf("echo '%s'", execPluginOutput)},
								},
							}}),
						},
					},
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(provider).NotTo(BeNil())
			})

			setupAndStartControllers()

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

				// Mock the control plane health condition and CredentialProviders
				profileMember.Status.CredentialProviders = []clusterinventoryv1alpha1.CredentialProvider{{
					Name: credentialProviderName,
					Cluster: clientcmdv1.Cluster{
						Server:                   cfgMember.Host,
						CertificateAuthorityData: cfgMember.CAData,
					},
				}}
				profileMember.Status.Conditions = append(profileMember.Status.Conditions, metav1.Condition{
					Type:               clusterinventoryv1alpha1.ClusterConditionControlPlaneHealthy,
					Status:             metav1.ConditionTrue,
					Reason:             "Healthy",
					Message:            "Control plane is mocked as healthy",
					LastTransitionTime: metav1.Now(),
				})
				Expect(cliHub.Status().Update(ctx, profileMember)).To(Succeed())
			})

			createObjects()
		})

		assertBasicControllerBehavior()
		assertClusterIndexBehavior()

		// No need to test for re-engaging the cluster since the kubeconfig is provided by the exec plugin

		AfterAll(func() {
			By("Stopping the provider, cluster, manager, and controller", func() {
				cancel()
			})
			By("Waiting for the error group to finish", func() {
				err := g.Wait()
				Expect(err).NotTo(HaveOccurred())
			})
			shutDownClusters()
		})
	})

	Context("Custom readiness check", Ordered, func() {
		const consumerName = "hub"
		var sa1TokenMember string

		BeforeAll(func() {
			ctx, cancel = context.WithCancel(context.Background())
			g, _ = errgroup.WithContext(ctx)

			createClusters()

			By("Setting up the Provider", func() {
				var err error
				provider, err = New(Options{
					KubeconfigStrategyOption: kubeconfigstrategy.Option{
						Secret: &kubeconfigstrategy.SecretStrategyOption{
							ConsumerName: consumerName,
						},
					},
					IsReady: func(ctx context.Context, clp *clusterinventoryv1alpha1.ClusterProfile) bool {
						return true
					},
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(provider).NotTo(BeNil())
			})

			setupAndStartControllers()

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

				sa1TokenMember = mustCreateAdminSAAndToken(ctx, cliMember, "sa1", "default")
				mustCreateOrUpdateKubeConfigSecretFromTokenSecret(
					ctx, cliHub, cfgMember,
					consumerName,
					*profileMember,
					sa1TokenMember,
				)
			})

			createObjects()
		})

		assertBasicControllerBehavior()
		assertClusterIndexBehavior()

		AfterAll(func() {
			By("Stopping the provider, cluster, manager, and controller", func() {
				cancel()
			})
			By("Waiting for the error group to finish", func() {
				err := g.Wait()
				Expect(err).NotTo(HaveOccurred())
			})
			shutDownClusters()
		})
	})
})

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func mustCreateAdminSAAndToken(ctx context.Context, cli client.Client, name, namespace string) string {
	sa := corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	Expect(client.IgnoreAlreadyExists(cli.Create(ctx, &sa))).To(Succeed())

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
	Expect(client.IgnoreAlreadyExists(cli.Create(ctx, &adminClusterRoleBinding))).To(Succeed())

	return tokenRequest.Status.Token
}

func mustCreateOrUpdateKubeConfigSecretFromTokenSecret(
	ctx context.Context,
	cli client.Client,
	cfg *rest.Config,
	consumerName string,
	clusterProfile clusterinventoryv1alpha1.ClusterProfile,
	token string,
) {
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
			kubeconfigstrategy.SecretLabelKeyClusterProfile:           clusterProfile.Name,
			kubeconfigstrategy.SecretLabelKeyClusterInventoryConsumer: consumerName,
		}
		kubeConfigSecret.StringData = map[string]string{
			kubeconfigstrategy.SecretDataKeyKubeConfig: kubeconfigStr,
		}
		return nil
	})
	Expect(err).NotTo(HaveOccurred())
}
