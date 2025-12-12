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

package manager

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// Coordinator controls per-cluster lifecycle for multicluster-aware runnables.
// Implementations may simply engage everything or perform sharding/fencing.
//
// EXPERIMENTAL: Coordinator is not a stable API and may change without notice.
type Coordinator interface {
	// AddRunnable registers a multicluster-aware component to be engaged for
	// clusters this coordinator activates. Safe to call before any Engage.
	AddRunnable(multicluster.Aware)

	// Engage informs the coordinator about a cluster that is now available.
	// The coordinator may start or delay runnables for this cluster based on
	// its policy. ctx is cancelled when the cluster is removed; implementations
	// should stop per-cluster work promptly on cancellation.
	Engage(ctx context.Context, name string, cl cluster.Cluster) error

	// Runnable returns an optional background Runnable driving coordination
	// (e.g., membership refresh, fencing). Return nil if no background work
	// is needed.
	Runnable() manager.Runnable
}

// WithCoordinator sets a custom Coordinator. EXPERIMENTAL: not a stable API.
func WithCoordinator(c Coordinator) Option {
	return func(m *mcManager) {
		m.coord = c
	}
}
