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

package clusterinventoryapi

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/go-logr/logr"
	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	"sigs.k8s.io/multicluster-runtime/providers/cluster-inventory-api/kubeconfigstrategy"
)

var _ multicluster.Provider = &Provider{}

// Options are the options for the Cluster Inventory API Provider.
type Options struct {
	// ClusterOptions are the options passed to the cluster constructor.
	ClusterOptions []cluster.Option

	// KubeconfigStrategyOption specifies options for kubeconfig strategy
	KubeconfigStrategyOption kubeconfigstrategy.Option

	// NewCluster is a function that creates a new cluster from a rest.Config.
	// The cluster will be started by the provider.
	NewCluster func(ctx context.Context, clp *clusterinventoryv1alpha1.ClusterProfile, cfg *rest.Config, opts ...cluster.Option) (cluster.Cluster, error)

	// IsReady is a function that determines if a cluster is ready.
	// If not provided, a default readiness check is used which checks for the
	// ControlPlaneHealthy condition on the ClusterProfile.
	IsReady func(ctx context.Context, clp *clusterinventoryv1alpha1.ClusterProfile) bool
}

type index struct {
	object       client.Object
	field        string
	extractValue client.IndexerFunc
}

// Provider is a cluster Provider that works with Cluster Inventory API.
type Provider struct {
	opts   Options
	log    logr.Logger
	client client.Client

	strategy kubeconfigstrategy.Interface

	lock       sync.RWMutex
	mcMgr      mcmanager.Manager
	clusters   map[string]cluster.Cluster
	cancelFns  map[string]context.CancelFunc
	kubeconfig map[string]*rest.Config
	indexers   []index
}

func setDefaults(opts *Options) {
	if opts.NewCluster == nil {
		opts.NewCluster = func(ctx context.Context, clp *clusterinventoryv1alpha1.ClusterProfile, cfg *rest.Config, opts ...cluster.Option) (cluster.Cluster, error) {
			return cluster.New(cfg, opts...)
		}
	}
	if opts.IsReady == nil {
		opts.IsReady = func(ctx context.Context, clp *clusterinventoryv1alpha1.ClusterProfile) bool {
			controlPlaneHealthyCondition := meta.FindStatusCondition(clp.Status.Conditions, clusterinventoryv1alpha1.ClusterConditionControlPlaneHealthy)
			return controlPlaneHealthyCondition != nil && controlPlaneHealthyCondition.Status == metav1.ConditionTrue
		}
	}
}

// New creates a new Cluster Inventory API cluster Provider.
// You must call SetupWithManager to set up the provider with the manager.
func New(opts Options) (*Provider, error) {
	logger := log.Log.WithName("cluster-inventory-api-cluster-provider")
	ctx := log.IntoContext(context.Background(), logger)
	strategy, err := kubeconfigstrategy.New(ctx, opts.KubeconfigStrategyOption)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubeconfig strategy: %w", err)
	}
	p := &Provider{
		opts:       opts,
		log:        logger,
		clusters:   map[string]cluster.Cluster{},
		cancelFns:  map[string]context.CancelFunc{},
		kubeconfig: map[string]*rest.Config{},
		strategy:   strategy,
	}
	setDefaults(&p.opts)
	return p, nil
}

// SetupWithManager sets up the provider with the manager.
func (p *Provider) SetupWithManager(mgr mcmanager.Manager) error {
	if mgr == nil {
		return fmt.Errorf("manager is nil")
	}
	p.mcMgr = mgr

	// Get the local manager from the multi-cluster manager.
	localMgr := mgr.GetLocalManager()
	if localMgr == nil {
		return fmt.Errorf("local manager is nil")
	}
	p.client = localMgr.GetClient()

	// Inherit manager defaults (scheme, etc.) so remote clusters recognise
	// the same types as the local manager.
	p.opts.ClusterOptions = append(mgr.DefaultClusterOptions(), p.opts.ClusterOptions...)

	// Create a controller builder
	controllerBuilder := builder.ControllerManagedBy(localMgr).
		For(&clusterinventoryv1alpha1.ClusterProfile{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}) // no parallelism.

	// Apply any custom watches provided by the user
	for _, customWatch := range p.strategy.CustomWatches() {
		controllerBuilder.Watches(
			customWatch.Object,
			customWatch.EventHandler,
			customWatch.Opts...,
		)
	}

	// Complete the controller setup
	if err := controllerBuilder.Complete(p); err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}

	return nil
}

// Get returns the cluster with the given name, if it is known.
func (p *Provider) Get(_ context.Context, clusterName string) (cluster.Cluster, error) {
	p.lock.RLock()
	defer p.lock.RUnlock()
	if cl, ok := p.clusters[clusterName]; ok {
		return cl, nil
	}

	return nil, multicluster.ErrClusterNotFound
}

// Reconcile is the reconcile loop for the Cluster Inventory API cluster Provider.
func (p *Provider) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	key := req.NamespacedName.String()

	log := p.log.WithValues("clusterprofile", key)
	log.Info("Reconciling ClusterProfile")

	// get the cluster
	clp := &clusterinventoryv1alpha1.ClusterProfile{}
	if err := p.client.Get(ctx, req.NamespacedName, clp); err != nil {
		if apierrors.IsNotFound(err) {
			log.Error(err, "failed to get cluster profile")

			p.lock.Lock()
			defer p.lock.Unlock()

			delete(p.clusters, key)
			if cancel, ok := p.cancelFns[key]; ok {
				cancel()
			}

			return reconcile.Result{}, nil
		}

		return reconcile.Result{}, fmt.Errorf("failed to get ClusterProfile %s: %w", key, err)
	}
	log.V(3).Info("Found ClusterProfile")

	p.lock.Lock()
	defer p.lock.Unlock()

	// provider already started?
	if p.mcMgr == nil {
		log.V(3).Info("Provider not started yet, requeuing")
		return reconcile.Result{RequeueAfter: time.Second * 2}, nil
	}

	// ready?
	if !p.opts.IsReady(ctx, clp) {
		log.Info("ClusterProfile is not healthy yet, requeuing")
		return reconcile.Result{RequeueAfter: time.Second * 10}, nil
	}

	// get kubeconfig
	cfg, err := p.strategy.GetKubeConfig(ctx, p.client, clp)
	if err != nil {
		log.Error(err, "Failed to get kubeconfig for ClusterProfile")
		return reconcile.Result{}, fmt.Errorf("failed to get kubeconfig for ClusterProfile=%s: %w", key, err)
	}

	// already engaged and kubeconfig is not changed?
	if _, ok := p.clusters[key]; ok {
		if p.kubeconfig[key] != nil && reflect.DeepEqual(p.kubeconfig[key], cfg) {
			log.Info("ClusterProfile already engaged and kubeconfig is unchanged, skipping")
			return reconcile.Result{}, nil
		}

		log.Info("ClusterProfile already engaged but kubeconfig is changed, re-engaging the ClusterProfile")
		// disengage existing cluster first if it exists.
		if cancel, ok := p.cancelFns[key]; ok {
			log.V(3).Info("Cancelling existing context for ClusterProfile")
			cancel()
			delete(p.clusters, key)
			delete(p.cancelFns, key)
			delete(p.kubeconfig, key)
		}
	}

	// create cluster.
	cl, err := p.opts.NewCluster(ctx, clp, cfg, p.opts.ClusterOptions...)
	if err != nil {
		log.Error(err, "Failed to create cluster for ClusterProfile")
		return reconcile.Result{}, fmt.Errorf("failed to create cluster for ClusterProfile=%s: %w", key, err)
	}
	for _, idx := range p.indexers {
		if err := cl.GetCache().IndexField(ctx, idx.object, idx.field, idx.extractValue); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to index field %q for %s=%s: %w", idx.field, idx.object.GetObjectKind().GroupVersionKind().String(), key, err)
		}
	}
	clusterCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := cl.Start(clusterCtx); err != nil {
			log.Error(err, "failed to start cluster for ClusterProfile")
			return
		}
	}()
	if !cl.GetCache().WaitForCacheSync(ctx) {
		cancel()
		log.Error(nil, "failed to sync cache for ClusterProfile")
		return reconcile.Result{}, fmt.Errorf("failed to sync cache for ClusterProfile=%s", key)
	}

	// remember.
	p.clusters[key] = cl
	p.cancelFns[key] = cancel
	p.kubeconfig[key] = cfg

	log.Info("Added new cluster for ClusterProfile")

	// engage manager.
	if err := p.mcMgr.Engage(clusterCtx, key, cl); err != nil {
		log.Error(err, "failed to engage manager for ClusterProfile")
		delete(p.clusters, key)
		delete(p.cancelFns, key)
		delete(p.kubeconfig, key)
		return reconcile.Result{}, err
	}

	log.Info("Cluster engaged manager for ClusterProfile")
	return reconcile.Result{}, nil
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
	for clusterProfileName, cl := range p.clusters {
		if err := cl.GetCache().IndexField(ctx, obj, field, extractValue); err != nil {
			p.log.Error(err, "Failed to index field on existing cluster", "field", field, "clusterprofile", clusterProfileName)
			return fmt.Errorf("failed to index field %q on ClusterProfile %q: %w", field, clusterProfileName, err)
		}
	}

	return nil
}
