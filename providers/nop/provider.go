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

package nop

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}
var _ multicluster.ProviderRunnable = &Provider{}

// Provider is a provider that does nothing.
type Provider struct{}

// New creates a new provider that does nothing.
func New() *Provider {
	return &Provider{}
}

// Start starts the provider and blocks.
func (p *Provider) Start(ctx context.Context, mcAware multicluster.Aware) error {
	<-ctx.Done()
	return nil
}

// Get returns an error for any cluster name.
func (p *Provider) Get(_ context.Context, clusterName string) (cluster.Cluster, error) {
	return nil, multicluster.ErrClusterNotFound
}

// IndexField does nothing.
func (p *Provider) IndexField(context.Context, client.Object, string, client.IndexerFunc) error {
	return nil
}
