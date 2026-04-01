/*
Copyright 2026 The Kubernetes Authors.

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

// This example demonstrates using an experimental shared cache with scoped clusters following
// the wildcard cache pattern from exp/cache.
//
// Key concepts:
// 1. A WildcardCache aggregates watches from multiple clusters into one cache
// 2. ScopedCache provides a filtered view for a single cluster
// 3. ScopedCluster implements cluster.Cluster using the scoped cache
// 4. Provider discovers Kind clusters and creates scoped clusters dynamically
// 5. Controllers use standard mcbuilder/mcreconcile patterns
//
// This is very close to controller-runtime while using a shared cache for
// memory efficiency.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	kind "sigs.k8s.io/kind/pkg/cluster"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	expcache "sigs.k8s.io/multicluster-runtime/exp/cache"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	"sigs.k8s.io/multicluster-runtime/pkg/clusters"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// =============================================================================
// Provider - Discovers Kind clusters and creates scoped clusters with shared cache
// =============================================================================

var _ multicluster.Provider = &SharedCacheProvider{}
var _ expcache.WildcardCacheProvider = &SharedCacheProvider{}

// SharedCacheProvider implements multicluster.Provider using a shared wildcard cache.
// It also implements expcache.WildcardCacheProvider to allow reconcilers to access
// all clusters' data for cross-cluster queries.
type SharedCacheProvider struct {
	wildcardCache expcache.WildcardCache
	clusters      clusters.Clusters[cluster.Cluster]
	scheme        *runtime.Scheme
	log           logr.Logger
	prefix        string

	clusterConfigs map[string]*rest.Config
}

// NewSharedCacheProvider creates a new provider that uses a shared wildcard cache.
func NewSharedCacheProvider(scheme *runtime.Scheme, prefix string) *SharedCacheProvider {
	return &SharedCacheProvider{
		wildcardCache: expcache.NewWildcardCache(expcache.WildcardCacheOptions{
			Scheme: scheme,
			Resync: 10 * time.Minute,
		}),
		clusters:       clusters.New[cluster.Cluster](),
		scheme:         scheme,
		log:            ctrl.Log.WithName("shared-cache-provider"),
		prefix:         prefix,
		clusterConfigs: make(map[string]*rest.Config),
	}
}

// Get returns a cluster by name.
func (p *SharedCacheProvider) Get(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	return p.clusters.Get(ctx, clusterName)
}

// GetWildcardCache returns the wildcard cache for cross-cluster queries.
// This implements the expcache.WildcardCacheProvider interface.
func (p *SharedCacheProvider) GetWildcardCache() expcache.WildcardCache {
	return p.wildcardCache
}

// IndexField adds an index to the wildcard cache.
func (p *SharedCacheProvider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	return p.wildcardCache.IndexField(ctx, obj, field, extractValue)
}

// Start discovers Kind clusters and starts the provider.
func (p *SharedCacheProvider) Start(ctx context.Context, aware multicluster.Aware) error {
	p.log.Info("Starting shared cache provider")

	// Discover Kind clusters
	kindProvider := kind.NewProvider()
	clusterList, err := kindProvider.List()
	if err != nil {
		return fmt.Errorf("failed to list Kind clusters: %w", err)
	}

	// Filter clusters by prefix
	var matchingClusters []string
	for _, name := range clusterList {
		if p.prefix == "" || len(name) >= len(p.prefix) && name[:len(p.prefix)] == p.prefix {
			matchingClusters = append(matchingClusters, name)
		}
	}

	if len(matchingClusters) == 0 {
		return fmt.Errorf("no Kind clusters found with prefix %q", p.prefix)
	}

	// Get kubeconfigs and add clusters to the wildcard cache
	for _, name := range matchingClusters {
		kc, err := kindProvider.KubeConfig(name, false)
		if err != nil {
			p.log.Error(err, "Failed to get kubeconfig", "cluster", name)
			continue
		}
		cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kc))
		if err != nil {
			p.log.Error(err, "Failed to parse kubeconfig", "cluster", name)
			continue
		}

		p.clusterConfigs[name] = cfg
		if err := p.wildcardCache.AddCluster(name, cfg); err != nil {
			p.log.Error(err, "Failed to add cluster to cache", "cluster", name)
			continue
		}
	}

	// Start the wildcard cache
	go func() {
		if err := p.wildcardCache.Start(ctx); err != nil {
			p.log.Error(err, "Wildcard cache error")
		}
	}()

	// Create scoped clusters for each discovered cluster
	for name, cfg := range p.clusterConfigs {
		cl, err := expcache.NewScopedCluster(name, cfg, p.wildcardCache, p.scheme)
		if err != nil {
			p.log.Error(err, "Failed to create scoped cluster", "cluster", name)
			continue
		}

		if err := p.clusters.Add(ctx, name, cl, aware); err != nil {
			p.log.Error(err, "Failed to add cluster", "cluster", name)
			continue
		}
		p.log.Info("Engaged cluster", "cluster", name)
	}

	// Wait for caches to sync
	if !p.wildcardCache.WaitForCacheSync(ctx) {
		return fmt.Errorf("failed to sync wildcard cache")
	}

	p.log.Info("All caches synced, provider running")
	<-ctx.Done()
	return nil
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	log := ctrl.Log.WithName("main")

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	// Create the shared cache provider
	provider := NewSharedCacheProvider(scheme, "")

	// Create the multicluster manager
	mgr, err := mcmanager.New(ctrl.GetConfigOrDie(), provider, mcmanager.Options{})
	if err != nil {
		log.Error(err, "Failed to create manager")
		os.Exit(1)
	}

	// Build a controller that watches ConfigMaps across all clusters
	err = mcbuilder.ControllerManagedBy(mgr).
		Named("configmap-controller").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			log := ctrl.LoggerFrom(ctx).WithValues(
				"cluster", req.ClusterName,
				"namespace", req.Namespace,
				"name", req.Name,
			)

			// Get the cluster for this request
			cl, err := mgr.GetCluster(ctx, req.ClusterName)
			if err != nil {
				return ctrl.Result{}, err
			}

			// Get the ConfigMap from the scoped cluster (only sees this cluster's objects)
			cm := &corev1.ConfigMap{}
			if err := cl.GetClient().Get(ctx, req.NamespacedName, cm); err != nil {
				log.Info("ConfigMap not found (likely deleted)")
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}

			log.Info("Reconciling ConfigMap",
				"resourceVersion", cm.ResourceVersion,
				"dataKeys", len(cm.Data),
			)

			// =================================================================
			// SCOPED ACCESS: List ConfigMaps from THIS cluster only
			// =================================================================
			scopedCache := cl.GetCache()
			scopedList := &corev1.ConfigMapList{}
			if err := scopedCache.List(ctx, scopedList); err != nil {
				log.Error(err, "Failed to list ConfigMaps from scoped cache")
				return ctrl.Result{}, err
			}
			log.Info("ConfigMaps in THIS cluster (scoped)", "count", len(scopedList.Items))

			// =================================================================
			// WILDCARD ACCESS: List ConfigMaps from ALL clusters
			// =================================================================
			// Check if the provider supports wildcard cache access
			if wcp, ok := mgr.GetProvider().(expcache.WildcardCacheProvider); ok {
				wildcardCache := wcp.GetWildcardCache()

				// List ALL ConfigMaps across ALL clusters
				allConfigMaps := &corev1.ConfigMapList{}
				if err := wildcardCache.List(ctx, allConfigMaps); err != nil {
					log.Error(err, "Failed to list ConfigMaps from wildcard cache")
					return ctrl.Result{}, err
				}

				log.Info("ConfigMaps across ALL clusters (wildcard)", "count", len(allConfigMaps.Items))

				// Group by cluster to show distribution
				clusterCounts := make(map[string]int)
				for _, item := range allConfigMaps.Items {
					clusterName := expcache.GetClusterName(&item)
					clusterCounts[clusterName]++
				}
				for clusterName, count := range clusterCounts {
					log.Info("ConfigMaps per cluster", "cluster", clusterName, "count", count)
				}

				// Example: Find the same ConfigMap in other clusters
				for _, item := range allConfigMaps.Items {
					itemCluster := expcache.GetClusterName(&item)
					if item.Namespace == cm.Namespace && item.Name == cm.Name && itemCluster != req.ClusterName {
						log.Info("Found same ConfigMap in another cluster",
							"otherCluster", itemCluster,
							"resourceVersion", item.ResourceVersion,
						)
					}
				}
			}

			return ctrl.Result{}, nil
		}))
	if err != nil {
		log.Error(err, "Failed to create controller")
		os.Exit(1)
	}

	log.Info("Starting manager...")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Manager error")
		os.Exit(1)
	}
}
