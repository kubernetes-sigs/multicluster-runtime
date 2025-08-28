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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	ctrl "sigs.k8s.io/controller-runtime"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	kubeconfigprovider "sigs.k8s.io/multicluster-runtime/providers/kubeconfig"
)

func main() {
	var namespace string
	var kubeconfigSecretLabel string
	var kubeconfigSecretKey string

	flag.StringVar(&namespace, "namespace", "default", "Namespace where kubeconfig secrets are stored")
	flag.StringVar(&kubeconfigSecretLabel, "kubeconfig-label", "sigs.k8s.io/multicluster-runtime-kubeconfig",
		"Label used to identify secrets containing kubeconfig data")
	flag.StringVar(&kubeconfigSecretKey, "kubeconfig-key", "kubeconfig", "Key in the secret data that contains the kubeconfig")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrllog.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	entryLog := ctrllog.Log.WithName("entrypoint")
	ctx := ctrl.SetupSignalHandler()

	entryLog.Info("Starting application", "namespace", namespace, "kubeconfigSecretLabel", kubeconfigSecretLabel)

	// Create the kubeconfig provider with options
	providerOpts := kubeconfigprovider.Options{
		Namespace:             namespace,
		KubeconfigSecretLabel: kubeconfigSecretLabel,
		KubeconfigSecretKey:   kubeconfigSecretKey,
	}

	// Create the provider first, then the manager with the provider
	provider := kubeconfigprovider.New(providerOpts)

	// Setup a cluster-aware Manager, with the provider to lookup clusters.
	managerOpts := mcmanager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0", // Disable metrics server
		},
	}

	// Create multicluster manager
	entryLog.Info("Creating manager")
	mgr, err := mcmanager.New(ctrl.GetConfigOrDie(), provider, managerOpts)
	if err != nil {
		entryLog.Error(err, "Unable to create manager")
		os.Exit(1)
	}

	// Setup provider controller with the manager.
	err = provider.SetupWithManager(ctx, mgr)
	if err != nil {
		entryLog.Error(err, "Unable to setup provider with manager")
		os.Exit(1)
	}

	// Create a controller that watches ConfigMaps and logs when they are found.
	err = mcbuilder.ControllerManagedBy(mgr).
		Named("multicluster-configmaps").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(
			func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
				log := ctrllog.FromContext(ctx).WithValues("cluster", req.ClusterName)
				log.Info("Reconciling ConfigMap")

				cl, err := mgr.GetCluster(ctx, req.ClusterName)
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

				log.Info("ConfigMap found", "namespace", cm.Namespace, "name", cm.Name, "cluster", req.ClusterName)

				return ctrl.Result{}, nil
			},
		))
	if err != nil {
		entryLog.Error(err, "unable to create controller")
		os.Exit(1)
	}

	// Start the manager.
	err = mgr.Start(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		entryLog.Error(err, "unable to start")
		os.Exit(1)
	}
}
