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
	"fmt"
	"net/http"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	"sigs.k8s.io/multicluster-runtime/pkg/manager/peers"
	"sigs.k8s.io/multicluster-runtime/pkg/manager/sharder"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// LocalCluster is the name of the local cluster.
const LocalCluster = ""

// Manager is a multi-cluster-aware manager, like the controller-runtime Cluster,
// but without the direct embedding of cluster.Cluster.
type Manager interface {
	// Add will set requested dependencies on the component, and cause the component to be
	// started when Start is called.
	// Depending on if a Runnable implements LeaderElectionRunnable interface, a Runnable can be run in either
	// non-leaderelection mode (always running) or leader election mode (managed by leader election if enabled).
	Add(Runnable) error

	// Elected is closed when this manager is elected leader of a group of
	// managers, either because it won a leader election or because no leader
	// election was configured.
	Elected() <-chan struct{}

	// AddMetricsServerExtraHandler adds an extra handler served on path to the http server that serves metrics.
	// Might be useful to register some diagnostic endpoints e.g. pprof.
	//
	// Note that these endpoints are meant to be sensitive and shouldn't be exposed publicly.
	//
	// If the simple path -> handler mapping offered here is not enough,
	// a new http server/listener should be added as Runnable to the manager via Add method.
	AddMetricsServerExtraHandler(path string, handler http.Handler) error

	// AddHealthzCheck allows you to add Healthz checker
	AddHealthzCheck(name string, check healthz.Checker) error

	// AddReadyzCheck allows you to add Readyz checker
	AddReadyzCheck(name string, check healthz.Checker) error

	// Start starts all registered Controllers and blocks until the context is cancelled.
	// Returns an error if there is an error starting any controller.
	//
	// If LeaderElection is used, the binary must be exited immediately after this returns,
	// otherwise components that need leader election might continue to run after the leader
	// lock was lost.
	Start(ctx context.Context) error

	// GetWebhookServer returns a webhook.Server
	GetWebhookServer() webhook.Server

	// GetLogger returns this manager's logger.
	GetLogger() logr.Logger

	// GetControllerOptions returns controller global configuration options.
	GetControllerOptions() config.Controller

	// GetCluster returns a cluster for the given identifying cluster name. Get
	// returns an existing cluster if it has been created before.
	// If no cluster is known to the provider under the given cluster name,
	// an error should be returned.
	GetCluster(ctx context.Context, clusterName string) (cluster.Cluster, error)

	// ClusterFromContext returns the default cluster set in the context.
	ClusterFromContext(ctx context.Context) (cluster.Cluster, error)

	// GetManager returns a manager for the given cluster name.
	GetManager(ctx context.Context, clusterName string) (manager.Manager, error)

	// GetLocalManager returns the underlying controller-runtime manager of the
	// host. This is equivalent to GetManager(LocalCluster).
	GetLocalManager() manager.Manager

	// GetProvider returns the multicluster provider, or nil if it is not set.
	GetProvider() multicluster.Provider

	// GetFieldIndexer returns a client.FieldIndexer that adds indexes to the
	// multicluster provider (if set) and the local manager.
	GetFieldIndexer() client.FieldIndexer

	multicluster.Aware
}

// Options are the arguments for creating a new Manager.
type Options = manager.Options

// Runnable allows a component to be started.
// It's very important that Start blocks until
// it's done running.
type Runnable interface {
	manager.Runnable
	multicluster.Aware
}

var _ Manager = &mcManager{}

type mcManager struct {
	manager.Manager
	provider multicluster.Provider
	engine   *synchronizationEngine
}

// New returns a new Manager for creating Controllers. The provider is used to
// discover and manage clusters. With a provider set to nil, the manager will
// behave like a regular controller-runtime manager.
func New(config *rest.Config, provider multicluster.Provider, opts manager.Options) (Manager, error) {
	mgr, err := manager.New(config, opts)
	if err != nil {
		return nil, err
	}
	return WithMultiCluster(mgr, provider)
}

// WithMultiCluster wraps a host manager to run multi-cluster controllers.
func WithMultiCluster(mgr manager.Manager, provider multicluster.Provider) (Manager, error) {
	cfg := SynchronizationConfig{
		FenceNS: "kube-system", FencePrefix: "mcr-shard", PerClusterLease: true,
		LeaseDuration: 20 * time.Second, LeaseRenew: 10 * time.Second, FenceThrottle: 750 * time.Millisecond,
		PeerPrefix: "mcr-peer", PeerWeight: 1, Probe: 5 * time.Second, Rehash: 15 * time.Second,
	}

	pr := peers.NewLeaseRegistry(mgr.GetClient(), cfg.FenceNS, cfg.PeerPrefix, "", cfg.PeerWeight, mgr.GetLogger())
	self := pr.Self()

	eng := newSynchronizationEngine(
		mgr.GetClient(), mgr.GetLogger(),
		sharder.NewHRW(), pr, self, cfg,
	)

	m := &mcManager{Manager: mgr, provider: provider, engine: eng}

	// Start synchronization loop as a manager Runnable.
	if err := mgr.Add(eng.Runnable()); err != nil {
		return nil, err
	}
	return m, nil
}

// GetCluster returns a cluster for the given identifying cluster name. Get
// returns an existing cluster if it has been created before.
// If no cluster is known to the provider under the given cluster name,
// an error should be returned.
func (m *mcManager) GetCluster(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	if clusterName == LocalCluster {
		return m.Manager, nil
	}
	if m.provider == nil {
		return nil, fmt.Errorf("no multicluster provider set, but cluster %q passed", clusterName)
	}
	return m.provider.Get(ctx, clusterName)
}

// ClusterFromContext returns the default cluster set in the context.
func (m *mcManager) ClusterFromContext(ctx context.Context) (cluster.Cluster, error) {
	clusterName, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		return nil, fmt.Errorf("no cluster set in context, use ReconcilerWithCluster helper when building the controller")
	}
	return m.GetCluster(ctx, clusterName)
}

// GetLocalManager returns the underlying controller-runtime manager of the host.
func (m *mcManager) GetLocalManager() manager.Manager {
	return m.Manager
}

// GetProvider returns the multicluster provider, or nil if it is not set.
func (m *mcManager) GetProvider() multicluster.Provider {
	return m.provider
}

// Add will set requested dependencies on the component, and cause the component to be
// started when Start is called.
func (m *mcManager) Add(r Runnable) error {
	m.engine.AddRunnable(r)
	return m.Manager.Add(r)
}

// Engage gets called when the component should start operations for the given
// Cluster. ctx is cancelled when the cluster is disengaged.
func (m *mcManager) Engage(ctx context.Context, name string, cl cluster.Cluster) error {
	return m.engine.Engage(ctx, name, cl)
}

func (m *mcManager) GetManager(ctx context.Context, clusterName string) (manager.Manager, error) {
	cl, err := m.GetCluster(ctx, clusterName)
	if err != nil {
		return nil, err
	}
	return &scopedManager{
		Manager: m,
		Cluster: cl,
	}, nil
}

type fieldIndexerFunc func(context.Context, client.Object, string, client.IndexerFunc) error

func (f fieldIndexerFunc) IndexField(ctx context.Context, obj client.Object, fieldName string, indexerFunc client.IndexerFunc) error {
	return f(ctx, obj, fieldName, indexerFunc)
}

// GetFieldIndexer returns a client.FieldIndexer that adds indexes to the
// multicluster provider (if set) and to the local cluster if not.
func (m *mcManager) GetFieldIndexer() client.FieldIndexer {
	return fieldIndexerFunc(func(ctx context.Context, obj client.Object, fieldName string, indexerFunc client.IndexerFunc) error {
		if m.provider != nil {
			if err := m.provider.IndexField(ctx, obj, fieldName, indexerFunc); err != nil {
				return fmt.Errorf("failed to index field %q on multi-cluster provider: %w", fieldName, err)
			}
			return nil
		}
		return m.Manager.GetFieldIndexer().IndexField(ctx, obj, fieldName, indexerFunc)
	})
}

var _ manager.Manager = &scopedManager{}

type scopedManager struct {
	Manager
	cluster.Cluster
}

// Add adds a Runnable to the manager.
func (p *scopedManager) Add(r manager.Runnable) error {
	return p.Manager.GetLocalManager().Add(r)
}

// Start starts the manager.
func (p *scopedManager) Start(ctx context.Context) error {
	return p.Manager.GetLocalManager().Start(ctx)
}

// GetFieldIndexer returns the field indexer.
func (p *scopedManager) GetFieldIndexer() client.FieldIndexer {
	return p.Cluster.GetFieldIndexer()
}
