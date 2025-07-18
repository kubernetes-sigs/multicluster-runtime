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

// Package clusterprofile provides a Kubernetes cluster provider that watches ClusterProfile
// resources and creates controller-runtime clusters for each using credential providers.
package clusterprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sync"

	"github.com/go-logr/logr"
	clusterinventory "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/credentials"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}

// New creates a new ClusterProfile provider.
func New(opts Options) *Provider {
	if opts.Scheme == nil {
		opts.Scheme = scheme.Scheme
	}

	if opts.CredentialsProvider == nil {
		opts.CredentialsProvider = credentials.New([]credentials.Provider{})
	}

	return &Provider{
		opts:     opts,
		log:      log.Log.WithName("clusterprofile-provider"),
		clusters: map[string]activeCluster{},
	}
}

// Options contains the configuration for the clusterprofile provider.
type Options struct {
	// Namespace is the cluster inventory namespace.
	Namespace string
	// Scheme is the scheme to use for the clusters.
	Scheme *runtime.Scheme
	// CredentialsProvider is the credentials provider for the cluster profiles.
	CredentialsProvider *credentials.CredentialsProvider
}

type index struct {
	object       client.Object
	field        string
	extractValue client.IndexerFunc
}

// Provider is a cluster provider that watches for ClusterProfile resources
// and engages clusters based on their credential providers.
type Provider struct {
	opts     Options
	log      logr.Logger
	lock     sync.RWMutex // protects clusters and indexers
	clusters map[string]activeCluster
	indexers []index
	mgr      mcmanager.Manager
}

type activeCluster struct {
	Cluster cluster.Cluster
	Context context.Context
	Cancel  context.CancelFunc
	Hash    string
}

func (p *Provider) getCluster(clusterName string) (activeCluster, bool) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	ac, exists := p.clusters[clusterName]
	return ac, exists
}

func (p *Provider) setCluster(clusterName string, ac activeCluster) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.clusters[clusterName] = ac
}

func (p *Provider) addIndexer(idx index) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.indexers = append(p.indexers, idx)
}

// Get returns the cluster with the given name, if it is known.
func (p *Provider) Get(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	ac, exists := p.getCluster(clusterName)
	if !exists {
		return nil, multicluster.ErrClusterNotFound
	}
	return ac.Cluster, nil
}

// SetupWithManager sets up the provider with the manager.
func (p *Provider) SetupWithManager(ctx context.Context, mgr mcmanager.Manager) error {
	log := p.log
	log.Info("Starting clusterprofile provider", "options", p.opts)

	if mgr == nil {
		return fmt.Errorf("manager is nil")
	}
	p.mgr = mgr

	// Get the local manager from the multicluster manager
	localMgr := mgr.GetLocalManager()
	if localMgr == nil {
		return fmt.Errorf("local manager is nil")
	}

	// Setup the controller to watch for cluster profiles.
	err := ctrl.NewControllerManagedBy(localMgr).
		For(&clusterinventory.ClusterProfile{}, builder.WithPredicates(predicate.NewPredicateFuncs(
			func(obj client.Object) bool {
				if p.opts.Namespace == "" {
					return true
				}
				return obj.GetNamespace() == p.opts.Namespace
			},
		))).
		Complete(p)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}

	return nil
}

// Reconcile is the main controller function that reconciles cluster profiles
func (p *Provider) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cp, err := p.getClusterProfile(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}

	if cp == nil || cp.DeletionTimestamp != nil {
		p.removeCluster(req.Name)
		return ctrl.Result{}, nil
	}

	clusterName := req.NamespacedName.String()
	log := p.log.WithValues("cluster", clusterName)

	hashStr, err := p.hashClusterProfile(cp)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Check if cluster exists and needs to be updated
	existingCluster, clusterExists := p.getCluster(clusterName)
	if clusterExists {
		if existingCluster.Hash == hashStr {
			log.Info("Cluster already exists and has the hash, skipping")
			return ctrl.Result{}, nil
		}
		// If the cluster exists and the configuration has changed,
		// remove it and continue to create a new cluster in its place.
		// Creating a new cluster will ensure all new configuration is applied.
		log.Info("Cluster already exists, updating it")
		p.removeCluster(clusterName)
	}

	restConfig, err := p.opts.CredentialsProvider.BuildConfigFromCP(cp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get REST config for cluster %q: %w", clusterName, err)
	}

	// Create and setup the new cluster
	if err := p.createAndEngageCluster(ctx, clusterName, restConfig, hashStr, log); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// getClusterProfile retrieves a cluster profile and handles not found errors
func (p *Provider) getClusterProfile(ctx context.Context, namespacedName client.ObjectKey) (*clusterinventory.ClusterProfile, error) {
	cp := &clusterinventory.ClusterProfile{}
	if err := p.mgr.GetLocalManager().GetClient().Get(ctx, namespacedName, cp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get cluster profile: %w", err)
	}
	return cp, nil
}

func (p *Provider) hashClusterProfile(cp *clusterinventory.ClusterProfile) (string, error) {
	hash := fnv.New32a()
	cpJSON, err := json.Marshal(cp)
	if err != nil {
		return "", fmt.Errorf("failed to marshal cluster profile: %w", err)
	}
	hash.Write(cpJSON)
	return fmt.Sprintf("%x", hash.Sum32()), nil
}

// createAndEngageCluster creates a new cluster, sets it up, stores it, and engages it with the manager
func (p *Provider) createAndEngageCluster(ctx context.Context, clusterName string, restConfig *rest.Config, hashStr string, log logr.Logger) error {
	// Create a new cluster
	log.Info("Creating new cluster from REST config")
	cl, err := cluster.New(restConfig, func(o *cluster.Options) {
		o.Scheme = p.opts.Scheme
	})
	if err != nil {
		return fmt.Errorf("failed to create cluster: %w", err)
	}

	// Apply field indexers
	if err := p.applyIndexers(ctx, cl); err != nil {
		return err
	}

	// Create a context that will be canceled when this cluster is removed
	clusterCtx, cancel := context.WithCancel(ctx)

	// Start the cluster
	go func() {
		if err := cl.Start(clusterCtx); err != nil {
			log.Error(err, "Failed to start cluster")
		}
	}()

	// Wait for cache to be ready
	log.Info("Waiting for cluster cache to be ready")
	if !cl.GetCache().WaitForCacheSync(clusterCtx) {
		cancel()
		return fmt.Errorf("failed to wait for cache sync")
	}
	log.Info("Cluster cache is ready")

	// Store the cluster
	p.setCluster(clusterName, activeCluster{
		Cluster: cl,
		Context: clusterCtx,
		Cancel:  cancel,
		Hash:    hashStr,
	})

	log.Info("Successfully added cluster")

	// Engage cluster so that the manager can start operating on the cluster
	if err := p.mgr.Engage(clusterCtx, clusterName, cl); err != nil {
		log.Error(err, "Failed to engage manager, removing cluster")
		p.removeCluster(clusterName)
		return fmt.Errorf("failed to engage manager: %w", err)
	}

	log.Info("Successfully engaged manager")
	return nil
}

// applyIndexers applies field indexers to a cluster
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

// IndexField indexes a field on all clusters, existing and future.
func (p *Provider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	// Save for future clusters
	p.addIndexer(index{
		object:       obj,
		field:        field,
		extractValue: extractValue,
	})

	// Apply to existing clusters
	p.lock.RLock()
	defer p.lock.RUnlock()

	for name, ac := range p.clusters {
		if err := ac.Cluster.GetFieldIndexer().IndexField(ctx, obj, field, extractValue); err != nil {
			return fmt.Errorf("failed to index field %q on cluster %q: %w", field, name, err)
		}
	}

	return nil
}

// ListClusters returns a list of all discovered clusters.
func (p *Provider) ListClusters() []string {
	p.lock.RLock()
	defer p.lock.RUnlock()

	result := make([]string, 0, len(p.clusters))
	for name := range p.clusters {
		result = append(result, name)
	}
	return result
}

// removeCluster removes a cluster by name with write lock and cleanup
func (p *Provider) removeCluster(clusterName string) {
	log := p.log.WithValues("cluster", clusterName)

	p.lock.Lock()
	ac, exists := p.clusters[clusterName]
	if !exists {
		p.lock.Unlock()
		log.Info("Cluster not found, nothing to remove")
		return
	}

	log.Info("Removing cluster")
	delete(p.clusters, clusterName)
	p.lock.Unlock()

	// Cancel the context to trigger cleanup for this cluster.
	// This is done outside the lock to avoid holding the lock for a long time.
	ac.Cancel()
	log.Info("Successfully removed cluster and cancelled cluster context")
}
