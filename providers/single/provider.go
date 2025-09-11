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

package single

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}

// Provider is a provider that engages the passed cluster.
type Provider struct {
	name string
	cl   cluster.Cluster
}

// New returns a provider engaging the passed cluster under the given name.
// It does not start the passed cluster.
func New(name string, cl cluster.Cluster) *Provider {
	return &Provider{
		name: name,
		cl:   cl,
	}
}

// Run starts the provider and blocks.
func (p *Provider) Run(ctx context.Context, mgr mcmanager.Manager) error {
	if err := mgr.Engage(ctx, p.name, p.cl); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// Get returns the cluster with the given name.
func (p *Provider) Get(_ context.Context, clusterName string) (cluster.Cluster, error) {
	if clusterName == p.name {
		return p.cl, nil
	}
	return nil, multicluster.ErrClusterNotFound
}

// IndexField calls IndexField on the single cluster.
func (p *Provider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	return p.cl.GetFieldIndexer().IndexField(ctx, obj, field, extractValue)
}
