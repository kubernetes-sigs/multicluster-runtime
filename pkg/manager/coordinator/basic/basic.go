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

package basic

import (
	"context"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// Coordinator engages all clusters immediately without sharding or fencing.
type Coordinator struct {
	mu        sync.Mutex
	runnables []multicluster.Aware
}

// New returns a basic coordinator that engages every cluster.
func New() *Coordinator {
	return &Coordinator{}
}

// AddRunnable registers a multicluster-aware runnable.
func (c *Coordinator) AddRunnable(r multicluster.Aware) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runnables = append(c.runnables, r)
}

// Engage runs Engage on all registered runnables for this cluster.
func (c *Coordinator) Engage(ctx context.Context, name string, cl cluster.Cluster) error {
	c.mu.Lock()
	rs := append([]multicluster.Aware(nil), c.runnables...)
	c.mu.Unlock()
	for _, r := range rs {
		if err := r.Engage(ctx, name, cl); err != nil {
			return err
		}
	}
	return nil
}

// Runnable returns nil because the basic coordinator has no background work.
func (c *Coordinator) Runnable() manager.Runnable { return nil }
