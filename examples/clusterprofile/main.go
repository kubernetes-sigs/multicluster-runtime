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

// This example demonstrates how to use the ClusterProfiles provider to create
// a multi-cluster operator that can watch and reconcile resources across
// multiple Kubernetes clusters using ClusterProfile resources.
//
// The operator watches for ClusterProfile resources in a specified namespace
// and automatically creates connections to the clusters they reference using
// configured credential providers.
//
// Usage:
//
//	go run main.go --namespace cluster-inventory --credential-providers-file clusterprofile-provider-file.json
//
// This will:
// 1. Set up the ClusterProfiles provider with credential providers
// 2. Watch for ClusterProfile resources in the specified namespace
// 3. Create a multi-cluster controller that watches ConfigMaps across all connected clusters
// 4. Log information about discovered ConfigMaps
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/cluster-inventory-api/pkg/credentials"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clusterinventory "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	clusterprofileprovider "sigs.k8s.io/multicluster-runtime/providers/clusterprofile"
)

func init() {
	runtime.Must(clusterinventory.AddToScheme(scheme.Scheme))
}

func main() {
	// Set up command line flags for credential providers configuration
	credentialsProviders := credentials.SetupProviderFileFlag()
	flag.Parse()

	// Define the namespace where ClusterProfile resources will be watched
	var clusterInventoryNamespace string
	flag.StringVar(&clusterInventoryNamespace, "namespace", "", "Cluster inventory namespace")

	// Set up logging options
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// Initialize logger and signal handler
	ctrllog.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	entryLog := ctrllog.Log.WithName("entrypoint")
	ctx := ctrl.SetupSignalHandler()

	entryLog.Info("Starting ClusterProfiles provider example", "namespace", clusterInventoryNamespace)

	// Load credential providers from configuration file
	cpCreds, err := credentials.NewFromFile(*credentialsProviders)
	if err != nil {
		log.Fatalf("Got error reading credentials providers: %v", err)
	}

	// Create the clusterprofiles provider with options
	providerOpts := clusterprofileprovider.Options{
		Namespace:           clusterInventoryNamespace,
		CredentialsProvider: cpCreds,
		Scheme:              scheme.Scheme,
	}

	// Create the provider instance
	provider := clusterprofileprovider.New(providerOpts)

	// Setup a cluster-aware Manager with the provider to lookup clusters
	managerOpts := manager.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0", // Disable metrics server
		},
	}

	// Create multicluster manager that will coordinate across all discovered clusters
	entryLog.Info("Creating multicluster manager")
	mgr, err := mcmanager.New(ctrl.GetConfigOrDie(), provider, managerOpts)
	if err != nil {
		entryLog.Error(err, "Unable to create manager")
		os.Exit(1)
	}

	// Setup provider controller with the manager to start watching ClusterProfile resources
	err = provider.SetupWithManager(ctx, mgr)
	if err != nil {
		entryLog.Error(err, "Unable to setup provider with manager")
		os.Exit(1)
	}

	// Create a multi-cluster controller that watches ConfigMaps across all connected clusters
	// This demonstrates how to build controllers that operate across multiple clusters
	err = mcbuilder.ControllerManagedBy(mgr).
		Named("multicluster-configmaps").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(
			func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
				log := ctrllog.FromContext(ctx).WithValues("cluster", req.ClusterName)
				log.Info("Reconciling ConfigMap")

				// Get the cluster client for the specific cluster where this resource lives
				cl, err := mgr.GetCluster(ctx, req.ClusterName)
				if err != nil {
					return reconcile.Result{}, err
				}

				// Retrieve the ConfigMap from the specific cluster
				cm := &corev1.ConfigMap{}
				if err := cl.GetClient().Get(ctx, req.Request.NamespacedName, cm); err != nil {
					if apierrors.IsNotFound(err) {
						return reconcile.Result{}, nil
					}
					return reconcile.Result{}, err
				}

				log.Info("ConfigMap found", "namespace", cm.Namespace, "name", cm.Name, "cluster", req.ClusterName)

				// Here you would add your multi-cluster reconciliation logic
				// For example:
				// - Sync resources across clusters
				// - Aggregate data from multiple clusters
				// - Implement cross-cluster policies

				return ctrl.Result{}, nil
			},
		))
	if err != nil {
		entryLog.Error(err, "unable to create controller")
		os.Exit(1)
	}

	// Start the manager - this will begin watching ClusterProfile resources
	// and automatically connect to discovered clusters
	entryLog.Info("Starting manager")
	err = mgr.Start(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		entryLog.Error(err, "unable to start")
		os.Exit(1)
	}
}
