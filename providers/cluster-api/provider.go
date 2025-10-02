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

package capi

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}
var _ multicluster.ProviderRunnable = &Provider{}

// Options are the options for the Cluster-API cluster Provider.
type Options struct {
	// ClusterOptions are the options passed to the cluste constructor.
	ClusterOptions []cluster.Option

	// GetSecret is a function that returns the kubeconfig secret for a cluster.
	GetSecret func(ctx context.Context, ccl *capiv1beta1.Cluster) (*rest.Config, error)
	// NewCluster is a function that creates a new cluster from a rest.Config.
	// The cluster will be started by the provider.
	NewCluster func(ctx context.Context, ccl *capiv1beta1.Cluster, cfg *rest.Config, opts ...cluster.Option) (cluster.Cluster, error)
}

func setDefaults(opts *Options, cli client.Client) {
	if opts.GetSecret == nil {
		opts.GetSecret = func(ctx context.Context, ccl *capiv1beta1.Cluster) (*rest.Config, error) {
			bs, err := utilkubeconfig.FromSecret(ctx, cli, types.NamespacedName{Name: ccl.Name, Namespace: ccl.Namespace})
			if err != nil {
				return nil, fmt.Errorf("failed to get kubeconfig: %w", err)
			}
			return clientcmd.RESTConfigFromKubeConfig(bs)
		}
	}
	if opts.NewCluster == nil {
		opts.NewCluster = func(ctx context.Context, ccl *capiv1beta1.Cluster, cfg *rest.Config, opts ...cluster.Option) (cluster.Cluster, error) {
			return cluster.New(cfg, opts...)
		}
	}
}

// New creates a new Cluster-API cluster Provider.
func New(localMgr manager.Manager, opts Options) (*Provider, error) {
	p := &Provider{
		opts:      opts,
		log:       log.Log.WithName("cluster-api-cluster-provider"),
		client:    localMgr.GetClient(),
		clusters:  map[string]cluster.Cluster{},
		cancelFns: map[string]context.CancelFunc{},
	}

	setDefaults(&p.opts, p.client)

	if err := builder.ControllerManagedBy(localMgr).
		For(&capiv1beta1.Cluster{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}). // no prallelism.
		Complete(p); err != nil {
		return nil, fmt.Errorf("failed to create controller: %w", err)
	}

	return p, nil
}

type index struct {
	object       client.Object
	field        string
	extractValue client.IndexerFunc
}

// Provider is a cluster Provider that works with Cluster-API.
type Provider struct {
	opts   Options
	log    logr.Logger
	client client.Client

	lock      sync.Mutex
	mcAware   multicluster.Aware
	clusters  map[string]cluster.Cluster
	cancelFns map[string]context.CancelFunc
	indexers  []index
}

// Get returns the cluster with the given name, if it is known.
func (p *Provider) Get(_ context.Context, clusterName string) (cluster.Cluster, error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	if cl, ok := p.clusters[clusterName]; ok {
		return cl, nil
	}

	return nil, multicluster.ErrClusterNotFound
}

// Start starts the provider and blocks.
func (p *Provider) Start(ctx context.Context, mcAware multicluster.Aware) error {
	p.lock.Lock()
	p.mcAware = mcAware
	p.lock.Unlock()

	p.log.Info("Starting Cluster-API cluster provider")

	<-ctx.Done()

	return ctx.Err()
}

func (p *Provider) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := p.log.WithValues("cluster", req.Name)
	log.Info("Reconciling Cluster")

	key := req.NamespacedName.String()

	// get the cluster
	ccl := &capiv1beta1.Cluster{}
	if err := p.client.Get(ctx, req.NamespacedName, ccl); err != nil {
		if apierrors.IsNotFound(err) {
			log.Error(err, "failed to get cluster")

			p.lock.Lock()
			defer p.lock.Unlock()

			delete(p.clusters, key)
			if cancel, ok := p.cancelFns[key]; ok {
				cancel()
			}

			return reconcile.Result{}, nil
		}

		return reconcile.Result{}, fmt.Errorf("failed to get cluster: %w", err)
	}

	p.lock.Lock()
	defer p.lock.Unlock()

	// TODO(sttts): do tighter logging.

	// provider already started?
	if p.mcAware == nil {
		return reconcile.Result{RequeueAfter: time.Second * 2}, nil
	}

	// already engaged?
	if _, ok := p.clusters[key]; ok {
		log.Info("Cluster already engaged")
		return reconcile.Result{}, nil
	}

	// ready and provisioned?
	if ph := ccl.Status.GetTypedPhase(); ph != capiv1beta1.ClusterPhaseProvisioned {
		log.Info("Cluster not provisioned yet", "phase", ph)
		return reconcile.Result{}, nil
	}

	// get kubeconfig secret.
	cfg, err := p.opts.GetSecret(ctx, ccl)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// create cluster.
	cl, err := p.opts.NewCluster(ctx, ccl, cfg, p.opts.ClusterOptions...)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to create cluster: %w", err)
	}
	for _, idx := range p.indexers {
		if err := cl.GetCache().IndexField(ctx, idx.object, idx.field, idx.extractValue); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to index field %q: %w", idx.field, err)
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
		return reconcile.Result{}, fmt.Errorf("failed to sync cache")
	}

	// remember.
	p.clusters[key] = cl
	p.cancelFns[key] = cancel

	p.log.Info("Added new cluster")

	// engage manager.
	if err := p.mcAware.Engage(clusterCtx, key, cl); err != nil {
		log.Error(err, "failed to engage manager")
		delete(p.clusters, key)
		delete(p.cancelFns, key)
		return reconcile.Result{}, err
	}

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
	for name, cl := range p.clusters {
		if err := cl.GetCache().IndexField(ctx, obj, field, extractValue); err != nil {
			return fmt.Errorf("failed to index field %q on cluster %q: %w", field, name, err)
		}
	}

	return nil
}
