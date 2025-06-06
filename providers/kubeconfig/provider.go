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
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"

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
		client:   nil, // Will be set in Run
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
}

type index struct {
	object       client.Object
	field        string
	extractValue client.IndexerFunc
}

// Provider is a cluster provider that watches for secrets containing kubeconfig data
// and engages clusters based on those kubeconfigs.
type Provider struct {
	opts           Options
	log            logr.Logger
	client         client.Client
	lock           sync.RWMutex // protects everything below.
	clusters       map[string]activeCluster
	indexers       []index
	secretInformer cache.Informer
}

type activeCluster struct {
	Cluster cluster.Cluster
	Context context.Context
	Cancel  context.CancelFunc
	Hash    string // hash of the kubeconfig
}

// Get returns the cluster with the given name, if it is known.
func (p *Provider) Get(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	if cl, ok := p.clusters[clusterName]; ok {
		return cl.Cluster, nil
	}

	return nil, fmt.Errorf("cluster %s not found", clusterName)
}

// Run starts the provider and blocks, watching for kubeconfig secrets.
func (p *Provider) Run(ctx context.Context, mgr mcmanager.Manager) error {
	log := p.log
	log.Info("Starting kubeconfig provider", "namespace", p.opts.Namespace, "label", p.opts.KubeconfigSecretLabel)

	// If client isn't set yet, get it from the manager
	if p.client == nil && mgr != nil {
		log.Info("Setting client from manager")
		p.client = mgr.GetLocalManager().GetClient()
		if p.client == nil {
			return fmt.Errorf("failed to get client from manager")
		}
	}

	// Get the informer for secrets
	secretInf, err := mgr.GetLocalManager().GetCache().GetInformer(ctx, &corev1.Secret{})
	if err != nil {
		return fmt.Errorf("failed to get secret informer: %w", err)
	}
	p.lock.Lock()
	p.secretInformer = secretInf
	p.lock.Unlock()

	// Add event handlers for secrets
	if _, err := secretInf.AddEventHandler(toolscache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				return false
			}
			// Only process secrets in our namespace with our label
			return secret.Namespace == p.opts.Namespace &&
				secret.Labels[p.opts.KubeconfigSecretLabel] == "true"
		},
		Handler: toolscache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				secret := obj.(*corev1.Secret)
				log.Info("Processing new secret", "name", secret.Name)
				if err := p.handleSecret(ctx, secret, mgr); err != nil {
					log.Error(err, "Failed to handle secret", "name", secret.Name)
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				secret := newObj.(*corev1.Secret)
				log.Info("Processing updated secret", "name", secret.Name)
				if err := p.handleSecret(ctx, secret, mgr); err != nil {
					log.Error(err, "Failed to handle secret", "name", secret.Name)
				}
			},
			DeleteFunc: func(obj interface{}) {
				secret := obj.(*corev1.Secret)
				log.Info("Processing deleted secret", "name", secret.Name)
				p.handleSecretDelete(secret)
			},
		},
	}); err != nil {
		return fmt.Errorf("failed to add event handlers: %w", err)
	}

	// Block until context is done
	<-ctx.Done()
	log.Info("Context cancelled, exiting provider")
	return ctx.Err()
}

// handleSecret processes a secret containing kubeconfig data
func (p *Provider) handleSecret(ctx context.Context, secret *corev1.Secret, mgr mcmanager.Manager) error {
	if secret == nil {
		return fmt.Errorf("received nil secret")
	}

	// Extract name to use as cluster name
	clusterName := secret.Name
	log := p.log.WithValues("cluster", clusterName, "secret", fmt.Sprintf("%s/%s", secret.Namespace, secret.Name))

	// Check if this secret has kubeconfig data
	kubeconfigData, ok := secret.Data[p.opts.KubeconfigSecretKey]
	if !ok {
		log.Info("Secret does not contain kubeconfig data", "key", p.opts.KubeconfigSecretKey)
		return nil
	}

	// Hash the kubeconfig
	hash := sha256.New()
	hash.Write(kubeconfigData)
	hashStr := hex.EncodeToString(hash.Sum(nil))

	// Check if cluster exists and remove it if it does
	p.lock.RLock()
	ac, clusterExists := p.clusters[clusterName]
	if clusterExists {
		if ac.Hash == hashStr {
			log.Info("Cluster already exists and has the same kubeconfig, skipping")
			p.lock.RUnlock()
			return nil
		}

		log.Info("Cluster already exists, updating it")
		if err := p.removeCluster(clusterName); err != nil {
			return fmt.Errorf("failed to remove existing cluster: %w", err)
		}
	}
	p.lock.RUnlock()

	// Parse the kubeconfig
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	// Create a new cluster
	log.Info("Creating new cluster from kubeconfig")
	cl, err := cluster.New(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create cluster: %w", err)
	}

	// Apply any field indexers
	p.lock.RLock()
	for _, idx := range p.indexers {
		if err := cl.GetFieldIndexer().IndexField(ctx, idx.object, idx.field, idx.extractValue); err != nil {
			p.lock.RUnlock()
			return fmt.Errorf("failed to index field %q: %w", idx.field, err)
		}
	}
	p.lock.RUnlock()

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
		cancel() // Cancel context before returning error
		return fmt.Errorf("failed to wait for cache sync")
	}
	log.Info("Cluster cache is ready")

	// Store the cluster
	p.lock.Lock()
	p.clusters[clusterName] = activeCluster{
		Cluster: cl,
		Context: clusterCtx,
		Cancel:  cancel,
		Hash:    hashStr,
	}
	p.lock.Unlock()

	log.Info("Successfully added cluster")

	// Engage the manager if provided
	if mgr != nil {
		if err := mgr.Engage(clusterCtx, clusterName, cl); err != nil {
			log.Error(err, "Failed to engage manager, removing cluster")
			p.lock.Lock()
			delete(p.clusters, clusterName)
			p.lock.Unlock()
			cancel() // Cancel the cluster context
			return fmt.Errorf("failed to engage manager: %w", err)
		}
		log.Info("Successfully engaged manager")
	}

	return nil
}

// handleSecretDelete handles the deletion of a secret
func (p *Provider) handleSecretDelete(secret *corev1.Secret) {
	if secret == nil {
		return
	}

	clusterName := secret.Name
	log := p.log.WithValues("cluster", clusterName)

	log.Info("Handling deleted secret")

	// Remove the cluster
	if err := p.removeCluster(clusterName); err != nil {
		log.Error(err, "Failed to remove cluster")
	}
}

// removeCluster removes a cluster by name
func (p *Provider) removeCluster(clusterName string) error {
	log := p.log.WithValues("cluster", clusterName)
	log.Info("Removing cluster")

	// Find the cluster and cancel function
	p.lock.RLock()
	ac, exists := p.clusters[clusterName]
	if !exists {
		p.lock.RUnlock()
		return fmt.Errorf("cluster %s not found", clusterName)
	}
	p.lock.RUnlock()

	// Cancel the context to trigger cleanup for this cluster
	ac.Cancel()
	log.Info("Cancelled cluster context")

	// Clean up our maps
	p.lock.Lock()
	delete(p.clusters, clusterName)
	p.lock.Unlock()

	log.Info("Successfully removed cluster")
	return nil
}

// IndexField indexes a field on all clusters, existing and future.
func (p *Provider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	p.lock.Lock()
	defer p.lock.Unlock()

	// Save for future clusters
	p.indexers = append(p.indexers, index{
		object:       obj,
		field:        field,
		extractValue: extractValue,
	})

	// Apply to existing clusters
	for name, ac := range p.clusters {
		if err := ac.Cluster.GetFieldIndexer().IndexField(ctx, obj, field, extractValue); err != nil {
			return fmt.Errorf("failed to index field %q on cluster %q: %w", field, name, err)
		}
	}

	return nil
}

// ListClusters returns a list of all discovered clusters.
func (p *Provider) ListClusters() map[string]cluster.Cluster {
	p.lock.RLock()
	defer p.lock.RUnlock()

	// Return a copy of the map to avoid race conditions
	result := make(map[string]cluster.Cluster, len(p.clusters))
	for k, v := range p.clusters {
		result[k] = v.Cluster
	}
	return result
}
