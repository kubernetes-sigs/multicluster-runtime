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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}

const (
	labelKeyClusterInventoryConsumer = "x-k8s.io/cluster-inventory-consumer"
	labelKeyClusterProfile           = "x-k8s.io/cluster-profile"
	dataKeyKubeConfig                = "Config" // data key in the Secret that contains the kubeconfig.
)

// KubeconfigStrategy defines how the kubeconfig for a cluster profile is managed.
// It is used to fetch the kubeconfig for a cluster profile and can be extended to support different strategies.
type KubeconfigStrategy struct {
	// GetKubeConfig is a function that returns the kubeconfig secret for a cluster profile.
	GetKubeConfig func(ctx context.Context, cli client.Client, clp *clusterinventoryv1alpha1.ClusterProfile) (*rest.Config, error)

	// CustomWatches can add custom watches to the provider controller
	CustomWatches []CustomWatch
}

// Options are the options for the Cluster-API cluster Provider.
type Options struct {
	// ConsumerName is the name of the consumer that will use the cluster inventory API.
	ConsumerName string

	// ClusterOptions are the options passed to the cluster constructor.
	ClusterOptions []cluster.Option

	// KubeconfigStrategy defines how the kubeconfig for the cluster profile is managed.
	// It is used to fetch the kubeconfig for a cluster profile and can be extended to support different strategies.
	// The default strategy is KubeconfigStrategySecret(consumerName) which fetches the kubeconfig from a Secret
	// labeled with "x-k8s.io/cluster-inventory-consumer" and "x-k8s.io/cluster-profile" labels.
	// This is the "Push Model via Credentials in Secret" as described in KEP-4322: ClusterProfile API.
	// ref: https://github.com/kubernetes/enhancements/blob/master/keps/sig-multicluster/4322-cluster-inventory/README.md#push-model-via-credentials-in-secret-not-recommended
	KubeconfigStrategy *KubeconfigStrategy

	// NewCluster is a function that creates a new cluster from a rest.Config.
	// The cluster will be started by the provider.
	NewCluster func(ctx context.Context, clp *clusterinventoryv1alpha1.ClusterProfile, cfg *rest.Config, opts ...cluster.Option) (cluster.Cluster, error)
}

// CustomWatch specifies a custom watch spec that can be added to the provider controller.
type CustomWatch struct {
	Object       client.Object
	EventHandler handler.TypedEventHandler[client.Object, reconcile.Request]
	Opts         []builder.WatchesOption
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

	lock       sync.RWMutex
	mcMgr      mcmanager.Manager
	clusters   map[string]cluster.Cluster
	cancelFns  map[string]context.CancelFunc
	kubeconfig map[string]*rest.Config
	indexers   []index
}

// KubeconfigStrategySecret returns a KubeconfigStrategy that fetches the kubeconfig from a Secret
// labeled with "x-k8s.io/cluster-inventory-consumer" and "x-k8s.io/cluster-profile" labels.
// This is the "Push Model via Credentials in Secret" as described in KEP-4322: ClusterProfile API.
// ref: https://github.com/kubernetes/enhancements/blob/master/keps/sig-multicluster/4322-cluster-inventory/README.md#push-model-via-credentials-in-secret-not-recommended
func KubeconfigStrategySecret(consumerName string) *KubeconfigStrategy {
	return &KubeconfigStrategy{
		GetKubeConfig: func(ctx context.Context, cli client.Client, clp *clusterinventoryv1alpha1.ClusterProfile) (*rest.Config, error) {
			secrets := corev1.SecretList{}
			if err := cli.List(ctx, &secrets, client.InNamespace(clp.Namespace), client.MatchingLabels{
				labelKeyClusterInventoryConsumer: consumerName,
				labelKeyClusterProfile:           clp.Name,
			}); err != nil {
				return nil, fmt.Errorf("failed to list secrets: %w", err)
			}

			if len(secrets.Items) == 0 {
				return nil, fmt.Errorf("no secrets found")
			}

			if len(secrets.Items) > 1 {
				return nil, fmt.Errorf("multiple secrets found, expected one, got %d", len(secrets.Items))
			}

			secret := secrets.Items[0]

			data, ok := secret.Data[dataKeyKubeConfig]
			if !ok {
				return nil, fmt.Errorf("secret %s/%s does not contain Config data", secret.Namespace, secret.Name)
			}
			return clientcmd.RESTConfigFromKubeConfig(data)
		},
		CustomWatches: []CustomWatch{CustomWatch{
			Object: &corev1.Secret{},
			EventHandler: handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				secret, ok := obj.(*corev1.Secret)
				if !ok {
					return nil
				}

				if secret.GetLabels() == nil ||
					secret.GetLabels()[labelKeyClusterInventoryConsumer] != consumerName ||
					secret.GetLabels()[labelKeyClusterProfile] == "" {
					return nil
				}

				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: secret.GetNamespace(),
						Name:      secret.GetLabels()[labelKeyClusterProfile],
					},
				}}
			}),
			Opts: []builder.WatchesOption{
				builder.WithPredicates(predicate.NewPredicateFuncs(func(object client.Object) bool {
					secret, ok := object.(*corev1.Secret)
					if !ok {
						return false
					}
					return secret.GetLabels()[labelKeyClusterInventoryConsumer] == consumerName &&
						secret.GetLabels()[labelKeyClusterProfile] != ""
				})),
			},
		}},
	}
}

func setDefaults(opts *Options, cli client.Client) {
	if opts.KubeconfigStrategy == nil {
		opts.KubeconfigStrategy = KubeconfigStrategySecret(opts.ConsumerName)
	}
	if opts.NewCluster == nil {
		opts.NewCluster = func(ctx context.Context, clp *clusterinventoryv1alpha1.ClusterProfile, cfg *rest.Config, opts ...cluster.Option) (cluster.Cluster, error) {
			return cluster.New(cfg, opts...)
		}
	}
}

// New creates a new Cluster Inventory API cluster Provider.
// You must call SetupWithManager to set up the provider with the manager.
func New(opts Options) *Provider {
	p := &Provider{
		opts:       opts,
		log:        log.Log.WithName("cluster-inventory-api-cluster-provider"),
		clusters:   map[string]cluster.Cluster{},
		cancelFns:  map[string]context.CancelFunc{},
		kubeconfig: map[string]*rest.Config{},
	}
	setDefaults(&p.opts, p.client)
	return p
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

	// Create a controller builder
	controllerBuilder := builder.ControllerManagedBy(localMgr).
		For(&clusterinventoryv1alpha1.ClusterProfile{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}) // no parallelism.

	// Apply any custom watches provided by the user
	for _, customWatch := range p.opts.KubeconfigStrategy.CustomWatches {
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
	controlPlaneHealthyCondition := meta.FindStatusCondition(clp.Status.Conditions, clusterinventoryv1alpha1.ClusterConditionControlPlaneHealthy)
	if controlPlaneHealthyCondition == nil || controlPlaneHealthyCondition.Status != metav1.ConditionTrue {
		log.Info("ClusterProfile is not healthy yet, requeuing")
		return reconcile.Result{RequeueAfter: time.Second * 10}, nil
	}

	// get kubeconfig
	cfg, err := p.opts.KubeconfigStrategy.GetKubeConfig(ctx, p.client, clp)
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
