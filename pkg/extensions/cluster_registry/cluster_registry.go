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

package cluster_registry

import (
	"context"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// ClusterRegistry - keep track of cluster names attached to manager.
type ClusterRegistry interface {
	multicluster.Aware
	manager.Runnable

	// ClusterNames - return list of cluster names currently connected to multicluster runtime.
	ClusterNames() []string
}

type clusterRegistry struct {
	mu       sync.RWMutex
	clusters map[string]cluster.Cluster
}

func New() ClusterRegistry {
	return &clusterRegistry{clusters: make(map[string]cluster.Cluster)}
}

// Engage is called by the multicluster manager when a new cluster is discovered.
func (r *clusterRegistry) Engage(ctx context.Context, name string, cl cluster.Cluster) error {
	r.mu.Lock()
	r.clusters[name] = cl
	r.mu.Unlock()

	go func() {
		<-ctx.Done()
		r.mu.Lock()
		delete(r.clusters, name)
		r.mu.Unlock()
	}()
	return nil
}

// Start satisfies controller-runtime’s Runnable; just block on ctx.
func (r *clusterRegistry) Start(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// ClusterNames returns the current set of cluster names.
func (r *clusterRegistry) ClusterNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.clusters))
	for name := range r.clusters {
		out = append(out, name)
	}
	return out
}
