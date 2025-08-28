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

package multi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/go-logr/logr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mctrl "sigs.k8s.io/multicluster-runtime"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}

// Options defines the options for the provider.
type Options struct {
	Separator string
}

// Provider is a multicluster.Provider that manages multiple providers.
type Provider struct {
	opts Options

	log logr.Logger
	mgr mctrl.Manager

	providerLock   sync.RWMutex
	providers      map[string]multicluster.Provider
	providerCancel map[string]context.CancelFunc
}

// New returns a new instance of the provider with the given options.
func New(opts Options) *Provider {
	p := new(Provider)

	p.opts = opts
	if p.opts.Separator == "" {
		p.opts.Separator = "#"
	}

	p.log = log.Log.WithName("multi-provider")

	p.providers = make(map[string]multicluster.Provider)
	p.providerCancel = make(map[string]context.CancelFunc)

	return p
}

// SetManager sets the manager for the provider.
func (p *Provider) SetManager(mgr mctrl.Manager) {
	if p.mgr != nil {
		p.log.Error(nil, "manager already set, overwriting")
	}
	p.mgr = mgr
}

func (p *Provider) splitClusterName(clusterName string) (string, string) {
	parts := strings.SplitN(clusterName, p.opts.Separator, 2)
	if len(parts) < 2 {
		return "", clusterName
	}
	return parts[0], parts[1]
}

// AddProvider adds a new provider with the given prefix.
//
// The startFunc is called to start the provider - starting the provider
// outside of startFunc is an error and will result in undefined
// behaviour.
// startFunc should block for as long as the provider is running,
// If startFunc returns an error the provider is removed and the error
// is returned.
func (p *Provider) AddProvider(ctx context.Context, prefix string, provider multicluster.Provider, startFunc func(context.Context, mctrl.Manager) error) error {
	ctx, cancel := context.WithCancel(ctx)

	p.providerLock.Lock()
	_, ok := p.providers[prefix]
	p.providerLock.Unlock()
	if ok {
		cancel()
		return fmt.Errorf("provider already exists for prefix %q", prefix)
	}

	var wrappedMgr mctrl.Manager
	if p.mgr == nil {
		p.log.Info("manager is nil, wrapped manager passed to start will be nil as well", "prefix", prefix)
	} else {
		wrappedMgr = &wrappedManager{
			Manager: p.mgr,
			prefix:  prefix,
			sep:     p.opts.Separator,
		}
	}

	p.providerLock.Lock()
	p.providers[prefix] = provider
	p.providerCancel[prefix] = cancel
	p.providerLock.Unlock()

	go func() {
		defer p.RemoveProvider(prefix)
		if err := startFunc(ctx, wrappedMgr); err != nil {
			cancel()
			p.log.Error(err, "error in provider", "prefix", prefix)
		}
	}()

	return nil
}

// RemoveProvider removes a provider from the manager and cancels its
// context.
//
// Warning: This can lead to dangling clusters if the provider is not
// using the context it is started with to engage the clusters it
// manages.
func (p *Provider) RemoveProvider(prefix string) {
	p.providerLock.Lock()
	defer p.providerLock.Unlock()
	if cancel, ok := p.providerCancel[prefix]; ok {
		cancel()
		delete(p.providers, prefix)
		delete(p.providerCancel, prefix)
	}
}

// Get returns a cluster by name.
func (p *Provider) Get(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	prefix, clusterName := p.splitClusterName(clusterName)

	p.providerLock.RLock()
	provider, ok := p.providers[prefix]
	p.providerLock.RUnlock()

	if !ok {
		return nil, fmt.Errorf("provider not found %q: %w", prefix, multicluster.ErrClusterNotFound)
	}

	return provider.Get(ctx, clusterName)
}

// IndexField indexes a field  on all providers and clusters and returns
// the aggregated errors.
func (p *Provider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	p.providerLock.RLock()
	defer p.providerLock.RUnlock()
	var errs error
	for prefix, provider := range p.providers {
		if err := provider.IndexField(ctx, obj, field, extractValue); err != nil {
			errs = errors.Join(
				errs,
				fmt.Errorf("failed to index field %q on cluster %q: %w", field, prefix, err),
			)
		}
	}
	return errs
}
