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

package kind

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	kind "sigs.k8s.io/kind/pkg/cluster"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}
var _ multicluster.ProviderRunnable = &Provider{}

// Options contains the configuration for the kind provider.
type Options struct {
	// Prefix is an optional prefix applied to filter kind clusters by name.
	Prefix string
	// ClusterOptions is the list of options to pass to the cluster object.
	ClusterOptions []cluster.Option
	// RESTOptions is the list of options to pass to the rest client.
	RESTOptions []func(cfg *rest.Config) error
}

// New creates a new kind cluster Provider.
func New(opts Options) *Provider {
	return &Provider{
		opts:      opts,
		log:       log.Log.WithName("kind-cluster-provider"),
		clusters:  map[string]cluster.Cluster{},
		cancelFns: map[string]context.CancelFunc{},
	}
}

type index struct {
	object       client.Object
	field        string
	extractValue client.IndexerFunc
}

// Provider is a cluster Provider that works with a local Kind instance.
type Provider struct {
	mcAware multicluster.Aware

	opts      Options
	log       logr.Logger
	lock      sync.RWMutex
	clusters  map[string]cluster.Cluster
	cancelFns map[string]context.CancelFunc
	indexers  []index
}

// Get returns the cluster with the given name, if it is known.
func (p *Provider) Get(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	p.lock.RLock()
	defer p.lock.RUnlock()
	if cl, ok := p.clusters[clusterName]; ok {
		return cl, nil
	}

	return nil, multicluster.ErrClusterNotFound
}

// Start starts the provider and blocks.
func (p *Provider) Start(ctx context.Context, mcAware multicluster.Aware) error {
	p.mcAware = mcAware

	p.log.Info("Starting kind cluster provider")

	provider := kind.NewProvider()

	// initial list to smoke test
	if _, err := provider.List(); err != nil {
		return err
	}

	return wait.PollUntilContextCancel(ctx, time.Second*2, true, func(ctx context.Context) (done bool, err error) {
		list, err := provider.List()
		if err != nil {
			p.log.Info("failed to list kind clusters", "error", err)
			return false, nil // keep going
		}

		// start new clusters
		for _, clusterName := range list {
			log := p.log.WithValues("cluster", clusterName)

			// skip?
			if p.opts.Prefix != "" && !strings.HasPrefix(clusterName, p.opts.Prefix) {
				continue
			}
			p.lock.RLock()
			if _, ok := p.clusters[clusterName]; ok {
				p.lock.RUnlock()
				continue
			}
			p.lock.RUnlock()

			// create a new cluster
			kubeconfig, err := provider.KubeConfig(clusterName, false)
			if err != nil {
				p.log.Info("failed to get kind kubeconfig", "error", err)
				return false, nil // keep going
			}
			cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
			if err != nil {
				p.log.Info("failed to create rest config", "error", err)
				return false, nil // keep going
			}
			for _, opt := range p.opts.RESTOptions {
				if err := opt(cfg); err != nil {
					p.log.Info("failed to apply REST Options", "error", err)
					return false, nil // keep going
				}
			}

			cl, err := cluster.New(cfg, p.opts.ClusterOptions...)
			if err != nil {
				p.log.Info("failed to create cluster", "error", err)
				return false, nil // keep going
			}
			for _, idx := range p.indexers {
				if err := cl.GetCache().IndexField(ctx, idx.object, idx.field, idx.extractValue); err != nil {
					return false, fmt.Errorf("failed to index field %q: %w", idx.field, err)
				}
			}
			clusterCtx, cancel := context.WithCancel(ctx)
			go func() {
				if err := cl.Start(clusterCtx); err != nil {
					log.Error(err, "failed to start cluster")
					return
				}
			}()
			if !cl.GetCache().WaitForCacheSync(ctx) {
				cancel()
				log.Info("failed to sync cache")
				return false, nil
			}

			// remember
			p.lock.Lock()
			p.clusters[clusterName] = cl
			p.cancelFns[clusterName] = cancel
			p.lock.Unlock()

			p.log.Info("Added new cluster", "cluster", clusterName)

			// engage manager
			if err := p.mcAware.Engage(clusterCtx, clusterName, cl); err != nil {
				log.Error(err, "failed to engage manager")
				p.lock.Lock()
				delete(p.clusters, clusterName)
				delete(p.cancelFns, clusterName)
				p.lock.Unlock()
				return false, nil
			}
		}

		// remove old clusters
		kindNames := sets.New(list...)
		p.lock.Lock()
		clusterNames := make([]string, 0, len(p.clusters))
		for name := range p.clusters {
			clusterNames = append(clusterNames, name)
		}
		p.lock.Unlock()
		for _, name := range clusterNames {
			if !kindNames.Has(name) {
				// stop and forget
				p.lock.Lock()
				p.cancelFns[name]()
				delete(p.clusters, name)
				delete(p.cancelFns, name)
				p.lock.Unlock()

				p.log.Info("Cluster removed", "cluster", name)
			}
		}

		return false, nil
	})
}

// IndexField indexes a field on all clusters, existing and future.
func (p *Provider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	p.lock.Lock()
	defer p.lock.Unlock()

	// save for future clusters.
	p.indexers = append(p.indexers, index{
		object:       obj,
		field:        field,
		extractValue: extractValue,
	})

	// apply to existing clusters.
	for name, cl := range p.clusters {
		if err := cl.GetCache().IndexField(ctx, obj, field, extractValue); err != nil {
			return fmt.Errorf("failed to index field %q on cluster %q: %w", field, name, err)
		}
	}

	return nil
}
