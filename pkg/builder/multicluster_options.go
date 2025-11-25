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

package builder

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcsource "sigs.k8s.io/multicluster-runtime/pkg/source"
)

// ClusterFilterFunc is a function that filters clusters.
type ClusterFilterFunc = mcsource.ClusterFilterFunc

// EngageOptions configures how the controller should engage with clusters
// when a provider is configured.
type EngageOptions struct {
	engageWithLocalCluster     *bool
	engageWithProviderClusters *bool
	clusterFilter              ClusterFilterFunc
}

// WithEngageWithLocalCluster configures whether the controller should engage
// with the local cluster of the manager (empty string). This defaults to false
// if a cluster provider is configured, and to true otherwise.
func WithEngageWithLocalCluster(engage bool) EngageOptions {
	return EngageOptions{
		engageWithLocalCluster: &engage,
	}
}

// WithEngageWithProviderClusters configured whether the controller should engage
// with the provider clusters of the manager. This defaults to true if a
// cluster provider is set, and has no effect otherwise.
func WithEngageWithProviderClusters(engage bool) EngageOptions {
	return EngageOptions{
		engageWithProviderClusters: &engage,
	}
}

// WithClusterFilter configures a filter function that determines
// which clusters the controller should engage with.
// The option applies only if WithEngageWithProviderClusters is true.
func WithClusterFilter(filter ClusterFilterFunc) EngageOptions {
	return EngageOptions{
		clusterFilter: filter,
	}
}

// WithClustersFromProvider configures the controller to only engage with
// the clusters provided by the given provider.
// If is a helper function that wraps WithClusterFilter and has the
// same constraints and mutually exclusive.
func WithClustersFromProvider(ctx context.Context, provider multicluster.Provider) EngageOptions {
	return EngageOptions{
		clusterFilter: func(clusterName string, cluster cluster.Cluster) bool {
			cl, err := provider.Get(ctx, clusterName)
			if err != nil {
				return false
			}
			return cl == cluster
		},
	}
}

// ApplyToFor applies this configuration to the given ForInput options.
func (w EngageOptions) ApplyToFor(opts *ForInput) {
	if w.engageWithLocalCluster != nil {
		val := *w.engageWithLocalCluster
		opts.engageWithLocalCluster = &val
	}
	if w.engageWithProviderClusters != nil {
		val := *w.engageWithProviderClusters
		opts.engageWithProviderClusters = &val
	}
	if w.clusterFilter != nil {
		opts.clusterFilter = w.clusterFilter
	}
}

// ApplyToOwns applies this configuration to the given OwnsInput options.
func (w EngageOptions) ApplyToOwns(opts *OwnsInput) {
	if w.engageWithLocalCluster != nil {
		val := *w.engageWithLocalCluster
		opts.engageWithLocalCluster = &val
	}
	if w.engageWithProviderClusters != nil {
		val := *w.engageWithProviderClusters
		opts.engageWithProviderClusters = &val
	}
	if w.clusterFilter != nil {
		opts.clusterFilter = w.clusterFilter
	}
}

// ApplyToWatches applies this configuration to the given WatchesInput options.
func (w EngageOptions) ApplyToWatches(opts untypedWatchesInput) {
	if w.engageWithLocalCluster != nil {
		opts.setEngageWithLocalCluster(*w.engageWithLocalCluster)
	}
	if w.engageWithProviderClusters != nil {
		opts.setEngageWithProviderClusters(*w.engageWithProviderClusters)
	}
	if w.clusterFilter != nil {
		opts.setClusterFilter(w.clusterFilter)
	}
}

func (w *WatchesInput[request]) setEngageWithLocalCluster(engage bool) {
	w.engageWithLocalCluster = &engage
}

func (w *WatchesInput[request]) setEngageWithProviderClusters(engage bool) {
	w.engageWithProviderClusters = &engage
}
