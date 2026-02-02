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

/*
Package cache provides a shared cache implementation for multi-cluster scenarios.

This package implements the "wildcard cache" pattern, which aggregates watches
from multiple Kubernetes clusters into a single shared informer per GVK. This
provides significant memory savings compared to having one cache per cluster.

# Architecture

The package provides the following key components:

  - WildcardCache: A cache that operates across multiple clusters, aggregating
    watches into shared informers. Objects are annotated with their source cluster.

  - ScopedCache: A filtered view of a WildcardCache for a single cluster. It
    implements cache.Cache and filters all operations to only see objects from
    the specified cluster.

  - ScopedInformer: Wraps a cache.Informer to filter events to a single cluster.
    Only passes events through to handlers if the object's cluster annotation
    matches the configured cluster name.

  - ScopedCluster: Implements cluster.Cluster using a ScopedCache. Provides a
    full cluster.Cluster interface while using the shared wildcard cache.

  - MultiClusterListerWatcher: A ListerWatcher implementation that aggregates
    List/Watch operations from multiple clusters.

# Usage Pattern

	// Create a wildcard cache
	wildcardCache := cache.NewWildcardCache(cache.WildcardCacheOptions{
	    Scheme: scheme,
	    Resync: 10 * time.Minute,
	})

	// Add clusters to the cache
	wildcardCache.AddCluster("cluster-1", cluster1Config)
	wildcardCache.AddCluster("cluster-2", cluster2Config)

	// Create scoped clusters for each cluster
	scopedCluster1, _ := cache.NewScopedCluster("cluster-1", cluster1Config, wildcardCache, scheme)
	scopedCluster2, _ := cache.NewScopedCluster("cluster-2", cluster2Config, wildcardCache, scheme)

	// Use scoped clusters with the multicluster provider interface
	// Each scoped cluster sees only objects from its own cluster

# Memory Efficiency

The wildcard cache pattern provides memory efficiency by:

 1. Using a single SharedIndexInformer per GVK across all clusters
 2. Indexing objects by cluster name for efficient filtering
 3. Sharing the same underlying store for all clusters

This is particularly beneficial when watching many clusters, as the memory
overhead is O(1) per GVK rather than O(N) where N is the number of clusters.

# Limitations

  - Objects in the cache are annotated with the cluster name annotation
    (multicluster.x-k8s.io/cluster-name). This annotation is added by the
    MultiClusterListerWatcher.

  - The default MultiClusterListerWatcher provides a basic implementation.
    For production use, you may want to implement a custom ListerWatcherFactory
    that properly handles watch operations with reconnection logic.

  - Event recorders for ScopedCluster are currently fake implementations.
    A production implementation would need to create recorders that target
    events to the correct cluster.
*/
package cache
