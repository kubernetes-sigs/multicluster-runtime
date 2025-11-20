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

package clusters

import (
	"context"
	"fmt"
	"sync"

	"github.com/go-logr/logr"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/multicluster-runtime/pkg/clusters"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}
var _ multicluster.ProviderRunnable = &Provider{}

// InputChannelSize is the size of the input channel used to queue
// clusters to be added to the provider.
var InputChannelSize = 10

// Provider is a provider that embeds clusters.Clusters.
//
// It showcases how to implement a multicluster.Provider using
// clusters.Clusters and can be used as a starting point for building
// custom providers.
// Other providers should utilize clusters.Clusters directly instead of
// this type, as this type is primarily for demonstration purposes and
// can lead to opaque errors as it e.g. overwrites the .Add method.
type Provider struct {
	clusters.Clusters[cluster.Cluster]
	log logr.Logger

	lock    sync.Mutex
	waiting map[string]cluster.Cluster
	input   chan item
}

type item struct {
	clusterName string
	cluster     cluster.Cluster
}

// New creates a new provider that embeds clusters.Clusters.
func New() *Provider {
	p := new(Provider)
	p.log = log.Log.WithName("clusters-cluster-provider")
	p.Clusters = clusters.New[cluster.Cluster]()
	p.Clusters.ErrorHandler = p.log.Error
	p.waiting = make(map[string]cluster.Cluster)
	return p
}

func (p *Provider) startOnce() error {
	p.lock.Lock()
	defer p.lock.Unlock()
	if p.input != nil {
		return fmt.Errorf("provider already started")
	}
	p.input = make(chan item, InputChannelSize)
	return nil
}

// Start starts the provider.
func (p *Provider) Start(ctx context.Context, aware multicluster.Aware) error {
	if err := p.startOnce(); err != nil {
		return err
	}

	p.log.Info("starting provider")
	for clusterName, cl := range p.waiting {
		p.log.Info("adding waiting cluster to provider", "clusterName", clusterName)
		if err := p.Clusters.AddOrReplace(ctx, clusterName, cl, aware); err != nil {
			p.log.Error(err, "error adding cluster", "clusterName", clusterName)
		}
	}
	p.lock.Lock()
	p.waiting = nil
	p.lock.Unlock()

	for {
		select {
		case <-ctx.Done():
			p.log.Info("stopping provider")
			return nil
		case it := <-p.input:
			p.log.Info("adding cluster to provider", "clusterName", it.clusterName)
			if err := p.Clusters.AddOrReplace(ctx, it.clusterName, it.cluster, aware); err != nil {
				p.log.Error(err, "error adding cluster", "clusterName", it.clusterName)
			}
		}
	}
}

// Add adds a new cluster to the provider. If the provider has not been
// started yet it queues the cluster to be added when the provider
// starts.
func (p *Provider) Add(ctx context.Context, clusterName string, cl cluster.Cluster) error {
	p.lock.Lock()
	defer p.lock.Unlock()
	if p.input != nil {
		p.log.Info("queueing cluster to be added to provider", "clusterName", clusterName)
		p.input <- item{
			clusterName: clusterName,
			cluster:     cl,
		}
		return nil
	}
	p.log.Info("adding cluster to waiting list", "clusterName", clusterName)
	p.waiting[clusterName] = cl
	return nil
}
