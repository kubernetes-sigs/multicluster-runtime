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

package chain

import (
	"context"
	"errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}

// Provider is a multicluster.Provider that chains multiple providers
// together.
type Provider struct {
	providers []multicluster.Provider
}

// New creates a new provider to chain multiple providers together.
// The providers are expected to be started.
func New(providers ...multicluster.Provider) *Provider {
	return &Provider{
		providers: providers,
	}
}

// Add adds a new provider to the chain.
func (p *Provider) Add(provider multicluster.Provider) {
	p.providers = append(p.providers, provider)
}

// Get iterates over the chained providers and calls Get on each of them
// in the order they were passed to New or added, returning the first
// found cluster.
// If any provider returns an unexpected error the error is returned
// immediately.
func (p *Provider) Get(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	for _, provider := range p.providers {
		cluster, err := provider.Get(ctx, clusterName)
		if err == nil {
			return cluster, nil
		}
		if !errors.Is(err, multicluster.ErrClusterNotFound) {
			return nil, err
		}
	}
	return nil, multicluster.ErrClusterNotFound
}

// IndexField iterates over the chained providers and calls IndexField
// on each of them. The errors are aggregated and returned.
func (p *Provider) IndexField(ctx context.Context, obj client.Object, field string, indexer client.IndexerFunc) error {
	var errs error
	for _, provider := range p.providers {
		errs = errors.Join(errs, provider.IndexField(ctx, obj, field, indexer))
	}
	return errs
}
