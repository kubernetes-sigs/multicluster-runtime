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

// Package kubeconfig provides a Kubernetes cluster provider that watches secrets
// containing kubeconfig data and creates controller-runtime clusters for each.
package kubeconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

const (
	// DefaultKubeconfigSecretLabel is the default label key to identify kubeconfig secrets
	DefaultKubeconfigSecretLabel = "sigs.k8s.io/multicluster-runtime-kubeconfig"

	// DefaultKubeconfigSecretKey is the default key in the secret data that contains the kubeconfig
	DefaultKubeconfigSecretKey = "kubeconfig"
)

var _ multicluster.Provider = &Provider{}

// New creates a new Kubeconfig Provider.
func New(opts Options) *Provider {
	// Set defaults
	if opts.KubeconfigSecretLabel == "" {
		opts.KubeconfigSecretLabel = DefaultKubeconfigSecretLabel
	}
	if opts.KubeconfigSecretKey == "" {
		opts.KubeconfigSecretKey = DefaultKubeconfigSecretKey
	}

	return &Provider{
		opts:     opts,
		log:      log.Log.WithName("kubeconfig-provider"),
		clusters: map[string]activeCluster{},
	}
}

// Options contains the configuration for the kubeconfig provider.
type Options struct {
	// Namespace is the namespace where kubeconfig secrets are stored.
	Namespace string
	// KubeconfigSecretLabel is the label used to identify secrets containing kubeconfig data.
	KubeconfigSecretLabel string
	// KubeconfigSecretKey is the key in the secret data that contains the kubeconfig.
	KubeconfigSecretKey string
	// ClusterOptions is the list of options to pass to the cluster object.
	ClusterOptions []cluster.Option
	// RESTOptions is the list of options to pass to the rest client.
	RESTOptions []func(cfg *rest.Config) error
}

type index struct {
	object       client.Object
	field        string
	extractValue client.IndexerFunc
}

// Provider is a cluster provider that watches for secrets containing kubeconfig data
// and engages clusters based on those kubeconfigs.
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
	Hash    string // hash of the kubeconfig
}

// getCluster retrieves a cluster by name with read lock
func (p *Provider) getCluster(clusterName string) (activeCluster, bool) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	ac, exists := p.clusters[clusterName]
	return ac, exists
}

// setCluster adds a cluster with write lock
func (p *Provider) setCluster(clusterName string, ac activeCluster) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.clusters[clusterName] = ac
}

// addIndexer adds an indexer with write lock
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
	log.Info("Starting kubeconfig provider", "options", p.opts)

	if mgr == nil {
		return fmt.Errorf("manager is nil")
	}
	p.mgr = mgr

	// Get the local manager from the multicluster manager
	localMgr := mgr.GetLocalManager()
	if localMgr == nil {
		return fmt.Errorf("local manager is nil")
	}

	// Setup the controller to watch for secrets containing kubeconfig data
	err := ctrl.NewControllerManagedBy(localMgr).
		For(&corev1.Secret{}, builder.WithPredicates(predicate.NewPredicateFuncs(
			// Only watch for secrets in the configured namespace and with the configured label
			func(obj client.Object) bool {
				return obj.GetNamespace() == p.opts.Namespace &&
					obj.GetLabels()[p.opts.KubeconfigSecretLabel] == "true"
			},
		))).
		Complete(p)
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}

	return nil
}

// Reconcile is the main controller function that reconciles secrets containing kubeconfig data
func (p *Provider) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Handle secret retrieval and basic validation
	secret, err := p.getSecret(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}
	if secret == nil {
		// Secret not found, remove cluster if it exists
		p.removeCluster(req.Name)
		return ctrl.Result{}, nil
	}

	// Extract cluster name and create logger
	clusterName := secret.Name
	log := p.log.WithValues("cluster", clusterName, "secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name))

	// Handle secret deletion, this is usually only hit if there is a finalizer on the secret.
	if secret.DeletionTimestamp != nil {
		p.removeCluster(clusterName)
		return ctrl.Result{}, nil
	}

	// Extract and validate kubeconfig data
	kubeconfigData, ok := secret.Data[p.opts.KubeconfigSecretKey]
	if !ok || len(kubeconfigData) == 0 {
		log.Info("Secret does not contain kubeconfig data, skipping", "key", p.opts.KubeconfigSecretKey)
		return ctrl.Result{}, nil
	}

	// Hash the kubeconfig for change detection
	hashStr := p.hashKubeconfig(kubeconfigData)

	// Check if cluster exists and needs to be updated
	existingCluster, clusterExists := p.getCluster(clusterName)
	if clusterExists {
		if existingCluster.Hash == hashStr {
			log.Info("Cluster already exists and has the same kubeconfig, skipping")
			return ctrl.Result{}, nil
		}
		// If the cluster exists and the kubeconfig has changed,
		// remove it and continue to create a new cluster in its place.
		// Creating a new cluster will ensure all new configuration is applied.
		log.Info("Cluster already exists, updating it")
		p.removeCluster(clusterName)
	}

	// Create and setup the new cluster
	if err := p.createAndEngageCluster(ctx, clusterName, kubeconfigData, hashStr, log); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// getSecret retrieves a secret and handles not found errors
func (p *Provider) getSecret(ctx context.Context, namespacedName client.ObjectKey) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	if err := p.mgr.GetLocalManager().GetClient().Get(ctx, namespacedName, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil // Secret not found is not an error
		}
		return nil, fmt.Errorf("failed to get secret: %w", err)
	}
	return secret, nil
}

// hashKubeconfig creates a hash of the kubeconfig data
func (p *Provider) hashKubeconfig(kubeconfigData []byte) string {
	hash := sha256.New()
	hash.Write(kubeconfigData)
	return hex.EncodeToString(hash.Sum(nil))
}

// createAndEngageCluster creates a new cluster, sets it up, stores it, and engages it with the manager
func (p *Provider) createAndEngageCluster(ctx context.Context, clusterName string, kubeconfigData []byte, hashStr string, log logr.Logger) error {
	// Parse the kubeconfig
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	// Apply REST options
	for _, opt := range p.opts.RESTOptions {
		if err := opt(restConfig); err != nil {
			return fmt.Errorf("failed to apply REST option: %w", err)
		}
	}

	// Create a new cluster
	log.Info("Creating new cluster from kubeconfig")
	cl, err := cluster.New(restConfig, p.opts.ClusterOptions...)
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

	log.Info("Successfully added cluster")

	// Engage cluster so that the manager can start operating on the cluster
	if err := p.mgr.Engage(clusterCtx, clusterName, cl); err != nil {
		cancel()
		return fmt.Errorf("failed to engage manager: %w", err)
	}

	log.Info("Successfully engaged manager")

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
