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

package informers

import (
	"context"

	"k8s.io/client-go/tools/cache"
)

// ScopeableSharedIndexInformer is an informer that knows how to scope itself down to one cluster,
// or act as an informer across all clusters.
type ScopeableSharedIndexInformer interface {
	// Cluster returns a SharedIndexInformer that only sees objects from the specified cluster.
	// The returned informer filters events to only include objects with the matching cluster annotation.
	Cluster(clusterName string) cache.SharedIndexInformer

	// ClusterWithContext returns a SharedIndexInformer that only sees objects from the specified cluster.
	// When the context is canceled, all event handlers registered through this informer will be removed.
	ClusterWithContext(ctx context.Context, clusterName string) cache.SharedIndexInformer

	// Embed the standard SharedIndexInformer interface for cross-cluster access.
	cache.SharedIndexInformer
}
