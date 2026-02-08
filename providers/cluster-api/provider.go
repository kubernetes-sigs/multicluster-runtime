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

// Package capi provides a Kubernetes cluster provider that watches Cluster API
// Cluster resources and creates controller-runtime clusters for each provisioned cluster.
package capi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sync"

	capiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	utilkubeconfig "sigs.k8s.io/cluster-api/util/kubeconfig"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}

// Options are the options for the Cluster API cluster Provider.
type Options struct {
	// ClusterOptions are the options passed to the cluster constructor.
	ClusterOptions []cluster.Option

	// GetSecret is a function that returns the raw kubeconfig bytes for a CAPI cluster.
	// Defaults to reading the CAPI-managed kubeconfig secret.
	GetSecret func(ctx context.Context, cl client.Client, ccl *capiv1beta1.Cluster) ([]byte, error)

	// NewCluster is a function that creates a new cluster from a rest.Config.
	// The cluster will be started by the provider.
	NewCluster func(ctx context.Context, ccl *capiv1beta1.Cluster, cfg *rest.Config, opts ...cluster.Option) (cluster.Cluster, error)

	// IsReady is a function that determines if a CAPI cluster is ready to be engaged.
	// If not provided, defaults to checking that the cluster phase is Provisioned.
	IsReady func(ctx context.Context, ccl *capiv1beta1.Cluster) bool
}

func setDefaults(opts *Options) {
	if opts.GetSecret == nil {
		opts.GetSecret = func(ctx context.Context, cl client.Client, ccl *capiv1beta1.Cluster) ([]byte, error) {
			bs, err := utilkubeconfig.FromSecret(ctx, cl, types.NamespacedName{Name: ccl.Name, Namespace: ccl.Namespace})
			if err != nil {
				return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
			}
			return bs, nil
		}
	}
	if opts.NewCluster == nil {
		opts.NewCluster = func(ctx context.Context, ccl *capiv1beta1.Cluster, cfg *rest.Config, opts ...cluster.Option) (cluster.Cluster, error) {
			return cluster.New(cfg, opts...)
		}
	}
	if opts.IsReady == nil {
		opts.IsReady = func(_ context.Context, ccl *capiv1beta1.Cluster) bool {
			return ccl.Status.GetTypedPhase() == capiv1beta1.ClusterPhaseProvisioned
		}
	}
}

type index struct {
	object       client.Object
	field        string
	extractValue client.IndexerFunc
}

type activeCluster struct {
	cluster cluster.Cluster
	cancel  context.CancelFunc
	hash    string
}

// Provider is a cluster Provider that works with Cluster API.
type Provider struct {
	opts   Options
	client client.Client

	lock     sync.RWMutex
	mcMgr    mcmanager.Manager
	clusters map[string]activeCluster
	indexers []index
}

// New creates a new Cluster API cluster Provider.
// You must call SetupWithManager to set up the provider with the manager.
func New(opts Options) *Provider {
	p := &Provider{
		opts:     opts,
		clusters: map[string]activeCluster{},
	}
	setDefaults(&p.opts)
	return p
}

// SetupWithManager sets up the provider with the manager.
func (p *Provider) SetupWithManager(mgr mcmanager.Manager) error {
	if mgr == nil {
		return fmt.Errorf("manager is nil")
	}
	p.mcMgr = mgr

	localMgr := mgr.GetLocalManager()
	if localMgr == nil {
		return fmt.Errorf("local manager is nil")
	}
	p.client = localMgr.GetClient()

	if err := builder.ControllerManagedBy(localMgr).
		For(&capiv1beta1.Cluster{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}). // no parallelism.
		Complete(p); err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}

	return nil
}

// Get returns the cluster with the given name, if it is known.
func (p *Provider) Get(_ context.Context, clusterName string) (cluster.Cluster, error) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	ac, ok := p.clusters[clusterName]
	if !ok {
		return nil, multicluster.ErrClusterNotFound
	}
	return ac.cluster, nil
}

// Reconcile reconciles CAPI Cluster resources.
func (p *Provider) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	key := req.NamespacedName.String()
	log := log.FromContext(ctx)
	log.Info("Reconciling Cluster")

	// Get the CAPI cluster.
	ccl := &capiv1beta1.Cluster{}
	if err := p.client.Get(ctx, req.NamespacedName, ccl); err != nil {
		if apierrors.IsNotFound(err) {
			p.removeCluster(ctx, key)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get Cluster %s: %w", key, err)
	}

	// Handle deletion.
	if ccl.DeletionTimestamp != nil {
		p.removeCluster(ctx, key)
		return reconcile.Result{}, nil
	}

	// Check readiness.
	if !p.opts.IsReady(ctx, ccl) {
		log.V(3).Info("Cluster not ready yet")
		return reconcile.Result{}, nil
	}

	// Get kubeconfig.
	kubeconfigData, err := p.opts.GetSecret(ctx, p.client, ccl)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get kubeconfig for Cluster %s: %w", key, err)
	}

	// Hash the kubeconfig for change detection.
	hashStr := p.hashKubeconfig(kubeconfigData)

	// Check if cluster already engaged and kubeconfig unchanged.
	p.lock.RLock()
	ac, exists := p.clusters[key]
	p.lock.RUnlock()

	if exists {
		if ac.hash == hashStr {
			log.V(3).Info("Cluster already engaged and kubeconfig unchanged")
			return reconcile.Result{}, nil
		}
		log.Info("Cluster kubeconfig changed, re-engaging")
		p.removeCluster(ctx, key)
	}

	// Parse the kubeconfig.
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to parse kubeconfig for Cluster %s: %w", key, err)
	}

	// Create and engage the cluster.
	if err := p.createAndEngageCluster(ctx, key, ccl, cfg, hashStr); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// hashKubeconfig creates a SHA256 hash of the kubeconfig data for change detection.
func (p *Provider) hashKubeconfig(kubeconfigData []byte) string {
	h := sha256.New()
	h.Write(kubeconfigData)
	return hex.EncodeToString(h.Sum(nil))
}

// createAndEngageCluster creates a new cluster, starts it, and engages it with the manager.
func (p *Provider) createAndEngageCluster(ctx context.Context, key string, ccl *capiv1beta1.Cluster, cfg *rest.Config, hashStr string) error {
	log := log.FromContext(ctx)
	log.Info("Creating new cluster")

	cl, err := p.opts.NewCluster(ctx, ccl, cfg, p.opts.ClusterOptions...)
	if err != nil {
		return fmt.Errorf("failed to create cluster for %s: %w", key, err)
	}

	// Apply field indexers.
	if err := p.applyIndexers(ctx, cl); err != nil {
		return err
	}

	// Start the cluster.
	clusterCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := cl.Start(clusterCtx); err != nil {
			log.Error(err, "Failed to start cluster")
		}
	}()

	// Wait for cache sync.
	if !cl.GetCache().WaitForCacheSync(clusterCtx) {
		cancel()
		return fmt.Errorf("failed to sync cache for Cluster %s", key)
	}

	// Store the cluster.
	p.lock.Lock()
	p.clusters[key] = activeCluster{
		cluster: cl,
		cancel:  cancel,
		hash:    hashStr,
	}
	p.lock.Unlock()

	log.Info("Cluster cache synced, engaging")

	// Engage with the manager.
	if err := p.mcMgr.Engage(clusterCtx, key, cl); err != nil {
		p.removeCluster(ctx, key)
		return fmt.Errorf("failed to engage Cluster %s: %w", key, err)
	}

	log.Info("Successfully engaged cluster")
	return nil
}

// applyIndexers applies all registered field indexers to a cluster.
func (p *Provider) applyIndexers(ctx context.Context, cl cluster.Cluster) error {
	p.lock.RLock()
	defer p.lock.RUnlock()

	for _, idx := range p.indexers {
		if err := cl.GetFieldIndexer().IndexField(ctx, idx.object, idx.field, idx.extractValue); err != nil {
			return fmt.Errorf("failed to index field %q: %w", idx.field, err)
		}
	}

	return nil
}

// removeCluster removes a cluster by name, cancelling its context.
func (p *Provider) removeCluster(ctx context.Context, key string) {
	log := log.FromContext(ctx)

	p.lock.Lock()
	ac, exists := p.clusters[key]
	if !exists {
		p.lock.Unlock()
		return
	}

	log.Info("Removing cluster")
	delete(p.clusters, key)
	p.lock.Unlock()

	// Cancel outside the lock to avoid holding it during cleanup.
	ac.cancel()
	log.Info("Successfully removed cluster")
}

// IndexField indexes a field on all clusters, existing and future.
func (p *Provider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	// Save for future clusters and clone current clusters under lock.
	p.lock.Lock()
	p.indexers = append(p.indexers, index{
		object:       obj,
		field:        field,
		extractValue: extractValue,
	})
	clusters := make(map[string]activeCluster, len(p.clusters))
	maps.Copy(clusters, p.clusters)
	p.lock.Unlock()

	// Apply to existing clusters outside the lock.
	for name, ac := range clusters {
		if err := ac.cluster.GetFieldIndexer().IndexField(ctx, obj, field, extractValue); err != nil {
			return fmt.Errorf("failed to index field %q on cluster %q: %w", field, name, err)
		}
	}

	return nil
}

// ListClusters returns the names of all engaged clusters.
func (p *Provider) ListClusters() []string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return slices.Collect(maps.Keys(p.clusters))
}
