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
	"maps"
	"strings"
	"sync"

	"github.com/go-logr/logr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}

// Options defines the options for the provider.
type Options struct {
	Separator   string
	ChannelSize int
}

// Provider is a multicluster.Provider that manages multiple providers.
type Provider struct {
	opts Options

	log logr.Logger

	lock            sync.RWMutex
	indexers        []index
	prefixCh        chan string
	providers       map[string]multicluster.Provider
	providersCancel map[string]context.CancelFunc
}

type index struct {
	Object    client.Object
	Field     string
	Extractor client.IndexerFunc
}

// New returns a new instance of the provider with the given options.
func New(opts Options) *Provider {
	p := new(Provider)

	p.opts = opts
	if p.opts.Separator == "" {
		p.opts.Separator = "#"
	}
	if p.opts.ChannelSize <= 0 {
		p.opts.ChannelSize = 10
	}

	p.log = log.Log.WithName("multi-provider")

	p.indexers = make([]index, 0)
	p.providers = make(map[string]multicluster.Provider)
	p.providersCancel = make(map[string]context.CancelFunc)

	return p
}

// Start runs the provider. It runs all providers that implement
// multicluster.ProviderRunnable, even those added after Start() has
// been called.
func (p *Provider) Start(ctx context.Context, aware multicluster.Aware) error {
	p.log.Info("starting multi provider")

	p.lock.Lock()
	p.prefixCh = make(chan string, p.opts.ChannelSize)
	prefixes := maps.Keys(p.providers)
	p.lock.Unlock()

	for prefix := range prefixes {
		p.startProvider(ctx, prefix, aware)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case prefix := <-p.prefixCh:
			p.startProvider(ctx, prefix, aware)
		}
	}
}

func (p *Provider) startProvider(ctx context.Context, prefix string, aware multicluster.Aware) {
	p.log.Info("starting provider", "prefix", prefix)

	p.lock.RLock()
	provider, ok := p.providers[prefix]
	p.lock.RUnlock()
	if !ok {
		p.log.Error(nil, "provider not found", "prefix", prefix)
		return
	}

	runnable, ok := provider.(multicluster.ProviderRunnable)
	if !ok {
		p.log.Info("provider is not runnable, not starting", "prefix", prefix)
		return
	}

	ctx, cancel := context.WithCancel(ctx)

	wrappedAware := &wrappedAware{
		Aware:  aware,
		prefix: prefix,
		sep:    p.opts.Separator,
	}

	p.lock.Lock()
	if _, ok := p.providersCancel[prefix]; ok {
		// This is a failsafe. It should never happen but on the off
		// change that it somehow does the provider shouldn't be started
		// twice.
		cancel()
		p.log.Error(nil, "provider already started, not starting again", "prefix", prefix)
		p.lock.Unlock()
		return
	}
	p.providersCancel[prefix] = cancel
	p.lock.Unlock()

	go func() {
		defer p.RemoveProvider(prefix)
		if err := runnable.Start(ctx, wrappedAware); err != nil {
			p.log.Error(err, "error in provider", "prefix", prefix)
		}
	}()

	p.lock.RLock()
	for _, indexer := range p.indexers {
		if err := provider.IndexField(ctx, indexer.Object, indexer.Field, indexer.Extractor); err != nil {
			p.log.Error(err, "failed to apply indexer to provider", "prefix", prefix, "object", fmt.Sprintf("%T", indexer.Object), "field", indexer.Field)
		}
	}
	p.lock.RUnlock()
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
func (p *Provider) AddProvider(prefix string, provider multicluster.Provider) error {
	p.lock.Lock()
	defer p.lock.Unlock()

	_, ok := p.providers[prefix]
	if ok {
		return fmt.Errorf("provider already exists for prefix %q", prefix)
	}

	p.log.Info("adding provider", "prefix", prefix)

	p.providers[prefix] = provider
	if p.prefixCh != nil {
		p.prefixCh <- prefix
	}

	return nil
}

// RemoveProvider removes a provider from the manager and cancels its
// context.
//
// Warning: This can lead to dangling clusters if the provider is not
// using the context it is started with to engage the clusters it
// manages.
func (p *Provider) RemoveProvider(prefix string) {
	p.lock.Lock()
	defer p.lock.Unlock()

	if cancel, ok := p.providersCancel[prefix]; ok {
		cancel()
		delete(p.providersCancel, prefix)
	}

	if _, ok := p.providers[prefix]; !ok {
		p.log.Info("provider not found when removing", "prefix", prefix)
	}
	delete(p.providers, prefix)
}

// Get returns a cluster by name.
func (p *Provider) Get(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	prefix, clusterName := p.splitClusterName(clusterName)
	p.log.V(1).Info("getting cluster", "prefix", prefix, "name", clusterName)

	p.lock.RLock()
	provider, ok := p.providers[prefix]
	p.lock.RUnlock()

	if !ok {
		p.log.Error(multicluster.ErrClusterNotFound, "provider not found for prefix", "prefix", prefix)
		return nil, fmt.Errorf("provider not found %q: %w", prefix, multicluster.ErrClusterNotFound)
	}

	return provider.Get(ctx, clusterName)
}

// IndexField indexes a field  on all providers and clusters and returns
// the aggregated errors.
func (p *Provider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.indexers = append(p.indexers, index{
		Object:    obj,
		Field:     field,
		Extractor: extractValue,
	})
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
