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

package main

import (
	"context"
	"errors"
	"flag"
	"os"

	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/credentials"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	clusterinventoryapi "sigs.k8s.io/multicluster-runtime/providers/cluster-inventory-api"
	"sigs.k8s.io/multicluster-runtime/providers/cluster-inventory-api/kubeconfigstrategy"
)

func init() {
	runtime.Must(clusterinventoryv1alpha1.AddToScheme(scheme.Scheme))
}

func main() {
	credentialsProviderFile := credentials.SetupProviderFileFlag()
	flag.Parse()

	ctrllog.SetLogger(zap.New(zap.UseDevMode(true)))
	entryLog := ctrllog.Log.WithName("entrypoint")
	ctx := signals.SetupSignalHandler()

	// Start local manager to read the Cluster Inventory API objects.
	cfg, err := ctrl.GetConfig()
	if err != nil {
		entryLog.Error(err, "unable to get kubeconfig")
		os.Exit(1)
	}

	// Load credential providers from configuration file
	credentialsProvider, err := credentials.NewFromFile(*credentialsProviderFile)
	if err != nil {
		entryLog.Error(err, "Got error reading credentials providers")
		os.Exit(1)
	}

	// Create the provider.
	provider, err := clusterinventoryapi.New(clusterinventoryapi.Options{
		// Specifying the strategy how to fetch kubeconfig from ClusterProfile.
		KubeconfigStrategyOption: kubeconfigstrategy.Option{
			CredentialsProvider: &kubeconfigstrategy.CredentialsProviderOption{
				Provider: credentialsProvider,
			},
			// // Alternative:
			// //   You can use the Secret strategy, but it is not recommended for production use.
			// Secret: &kubeconfigstrategy.SecretStrategyOption{
			// 	ConsumerName: "cluster-inventory-api-consumer",
			// },
		},
	})
	if err != nil {
		entryLog.Error(err, "unable to create provider")
		os.Exit(1)
	}

	// Create a multi-cluster manager attached to the provider.
	entryLog.Info("Setting up manager")
	mcMgr, err := mcmanager.New(cfg, provider, manager.Options{
		LeaderElection: false,
		Metrics: metricsserver.Options{
			BindAddress: "0", // only one can listen
		},
	})
	if err != nil {
		entryLog.Error(err, "unable to set up overall controller manager")
		os.Exit(1)
	}

	// Setting up the provider with multi-cluster manager.
	if err := provider.SetupWithManager(mcMgr); err != nil {
		entryLog.Error(err, "unable to set up provider with manager")
		os.Exit(1)
	}

	// Create a configmap controller in the multi-cluster manager.
	if err := mcbuilder.ControllerManagedBy(mcMgr).
		Named("multicluster-configmaps").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(
			func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
				log := ctrllog.FromContext(ctx).WithValues("cluster", req.ClusterName)
				log.Info("Reconciling ConfigMap")

				cl, err := mcMgr.GetCluster(ctx, req.ClusterName)
				if err != nil {
					return reconcile.Result{}, err
				}

				cm := &corev1.ConfigMap{}
				if err := cl.GetClient().Get(ctx, req.Request.NamespacedName, cm); err != nil {
					if apierrors.IsNotFound(err) {
						return reconcile.Result{}, nil
					}
					return reconcile.Result{}, err
				}

				log.Info("ConfigMap in cluster", "configmap", cm.Namespace+"/"+cm.Name, "cluster", req.ClusterName)

				return ctrl.Result{}, nil
			},
		)); err != nil {
		entryLog.Error(err, "failed to build controller")
		os.Exit(1)
	}

	if err := mcMgr.Start(ctx); ignoreCanceled(err) != nil {
		entryLog.Error(err, "unable to start")
		os.Exit(1)
	}
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
