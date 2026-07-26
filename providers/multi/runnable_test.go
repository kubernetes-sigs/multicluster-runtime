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

	"golang.org/x/sync/errgroup"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	kubeconfigprovider "sigs.k8s.io/multicluster-runtime/providers/kubeconfig"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	autoNamespace   = "multi-kubeconfig"
	autoSecretLabel = "multi.multicluster.io/kubeconfig"
	autoSecretKey   = "config"
	autoWorkloadNS  = "default"
	autoWorkloadTyp = "thing"
	autoTypeKey     = "type"
)

var _ = Describe("Multi with AsRunnable-wrapped provider", Ordered, func() {
	ctx, cancel := context.WithCancel(context.Background())
	g, ctx := errgroup.WithContext(ctx)

	testTimeout := "10s"

	var provider *Provider
	var mgr mcmanager.Manager
	var localCli, cloudCli client.Client

	BeforeAll(func() {
		var err error
		localCli, err = client.New(localCfg, client.Options{})
		Expect(err).NotTo(HaveOccurred())
		cloudCli, err = client.New(cloud1cfg, client.Options{})
		Expect(err).NotTo(HaveOccurred())

		By("Creating the secret namespace and a workload in the remote cluster", func() {
			Expect(client.IgnoreAlreadyExists(localCli.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: autoNamespace},
			}))).To(Succeed())
			Expect(client.IgnoreAlreadyExists(cloudCli.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: autoWorkloadNS, Name: "widget", Labels: map[string]string{autoTypeKey: autoWorkloadTyp}},
			}))).To(Succeed())
		})

		By("Setting up the multi provider and manager", func() {
			provider = New(Options{LoggerSuffix: "auto"})
			mgr, err = mcmanager.New(localCfg, provider, manager.Options{})
			Expect(err).NotTo(HaveOccurred())
		})

		By("Starting the manager", func() {
			g.Go(func() error {
				return ignoreCanceled(mgr.Start(ctx))
			})
		})

		By("Registering an index before the provider is added", func() {
			err := mgr.GetFieldIndexer().IndexField(ctx, &corev1.ConfigMap{}, autoTypeKey, func(obj client.Object) []string {
				return []string{obj.GetLabels()[autoTypeKey]}
			})
			Expect(err).NotTo(HaveOccurred())
		})

		By("Adding the kubeconfig provider wrapped with AsRunnable", func() {
			kc := kubeconfigprovider.New(kubeconfigprovider.Options{
				Namespace:             autoNamespace,
				KubeconfigSecretLabel: autoSecretLabel,
				KubeconfigSecretKey:   autoSecretKey,
			})
			Expect(provider.AddProvider("kc", AsRunnable(kc, mgr))).To(Succeed())
		})

		By("Creating the kubeconfig secret pointing at the remote cluster", func() {
			Expect(createKubeconfigSecret(ctx, "remote", cloud1cfg, localCli)).To(Succeed())
		})
	})

	It("engages the remote cluster under the prefixed name", func(ctx context.Context) {
		Eventually(func(g Gomega) {
			cl, err := mgr.GetCluster(ctx, "kc#remote")
			g.Expect(err).NotTo(HaveOccurred())
			cm := &corev1.ConfigMap{}
			g.Expect(cl.GetClient().Get(ctx, client.ObjectKey{Namespace: autoWorkloadNS, Name: "widget"}, cm)).To(Succeed())
		}, testTimeout).Should(Succeed())
	})

	It("propagates the multi-cluster index to the engaged cluster", func(ctx context.Context) {
		Eventually(func(g Gomega) []string {
			cl, err := mgr.GetCluster(ctx, "kc#remote")
			g.Expect(err).NotTo(HaveOccurred())
			cms := &corev1.ConfigMapList{}
			g.Expect(cl.GetCache().List(ctx, cms, client.MatchingFields{autoTypeKey: autoWorkloadTyp})).To(Succeed())
			names := make([]string, 0, len(cms.Items))
			for _, cm := range cms.Items {
				names = append(names, cm.Name)
			}
			return names
		}, testTimeout).Should(ContainElement("widget"))
	})

	AfterAll(func() {
		cancel()
		Expect(g.Wait()).NotTo(HaveOccurred())
	})
})

func createKubeconfigSecret(ctx context.Context, name string, cfg *rest.Config, cl client.Client) error {
	apiConfig := api.Config{
		Clusters: map[string]*api.Cluster{
			name: {Server: cfg.Host, CertificateAuthorityData: cfg.CAData},
		},
		AuthInfos: map[string]*api.AuthInfo{
			name: {
				ClientCertificateData: cfg.CertData,
				ClientKeyData:         cfg.KeyData,
				Token:                 cfg.BearerToken,
			},
		},
		Contexts: map[string]*api.Context{
			name: {Cluster: name, AuthInfo: name},
		},
		CurrentContext: name,
	}
	kubeconfigData, err := clientcmd.Write(apiConfig)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: autoNamespace,
			Labels:    map[string]string{autoSecretLabel: "true"},
		},
		Data: map[string][]byte{autoSecretKey: kubeconfigData},
	}
	return cl.Create(ctx, secret)
}
