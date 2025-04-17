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

package gardener

import (
	"context"
	"fmt"
	"sync"
	"time"

	authenticationv1alpha1 "github.com/gardener/gardener/pkg/apis/authentication/v1alpha1"
	gardencorev1beta1 "github.com/gardener/gardener/pkg/apis/core/v1beta1"
	extensionsv1alpha1 "github.com/gardener/gardener/pkg/apis/extensions/v1alpha1"
	resourcesv1alpha1 "github.com/gardener/gardener/pkg/apis/resources/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

func init() {
	runtime.Must(gardencorev1beta1.AddToScheme(scheme.Scheme))
	runtime.Must(extensionsv1alpha1.AddToScheme(scheme.Scheme))
}

var _ multicluster.Provider = &Provider{}

// Topology is a string alias for describing where the controller is running.
type Topology string

const (
	// TopologyGarden means the controller is running in the garden cluster or uses a garden kubeconfig.
	TopologyGarden Topology = "garden"
	// TopologySeed means the controller is running in a seed cluster or uses a seed kubeconfig.
	TopologySeed Topology = "seed"
)

// Options are the options for the Gardener Provider.
type Options struct {
	// ClusterOptions are the options passed to the cluster constructor.
	ClusterOptions []cluster.Option
	// Topology describes where the controller is running.
	Topology Topology
	// ExpirationSeconds is the maximum duration for kubeconfigs when using topology 'garden'.
	ExpirationSeconds *int64
}

// New creates a new Gardener cluster Provider.
func New(localMgr manager.Manager, opts Options) (*Provider, error) {
	p := &Provider{
		opts:      opts,
		log:       log.Log.WithName("gardener-shoot-provider"),
		client:    localMgr.GetClient(),
		clusters:  map[string]shoot{},
		cancelFns: map[string]context.CancelFunc{},
	}

	switch opts.Topology {
	case TopologyGarden:
		p.newObject = func() client.Object { return &gardencorev1beta1.Shoot{} }
		p.getKubeconfig = func(ctx context.Context, obj client.Object) (*rest.Config, time.Time, error) {
			adminKubeconfigRequest := &authenticationv1alpha1.AdminKubeconfigRequest{
				Spec: authenticationv1alpha1.AdminKubeconfigRequestSpec{
					ExpirationSeconds: opts.ExpirationSeconds,
				},
			}
			if err := localMgr.GetClient().SubResource("adminkubeconfig").Create(ctx, obj, adminKubeconfigRequest); err != nil {
				return nil, time.Time{}, fmt.Errorf("failed to create admin kubeconfig request for shoot %s: %w", client.ObjectKeyFromObject(obj), err)
			}

			restConfig, err := clientcmd.RESTConfigFromKubeConfig(adminKubeconfigRequest.Status.Kubeconfig)
			if err != nil {
				return nil, time.Time{}, fmt.Errorf("failed to get REST config from kubeconfig: %w", err)
			}

			return restConfig, adminKubeconfigRequest.Status.ExpirationTimestamp.Time.Add(-5 * time.Minute), nil
		}
		p.checkReadiness = func(_ context.Context, _ client.Client, object client.Object) (bool, error) {
			s, ok := object.(*gardencorev1beta1.Shoot)
			if !ok {
				return false, fmt.Errorf("expected *gardencorev1beta1.Shoot but got %T", object)
			}
			return s.Status.LastOperation != nil && (s.Status.LastOperation.Type != gardencorev1beta1.LastOperationTypeCreate || s.Status.LastOperation.State == gardencorev1beta1.LastOperationStateSucceeded), nil
		}

	case TopologySeed:
		secretName := "gardener"
		p.newObject = func() client.Object { return &extensionsv1alpha1.Cluster{} }
		p.getKubeconfig = func(ctx context.Context, obj client.Object) (*rest.Config, time.Time, error) {
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: obj.GetName()}}
			if err := localMgr.GetClient().Get(ctx, client.ObjectKeyFromObject(secret), secret); err != nil {
				return nil, time.Time{}, fmt.Errorf("failed to get kubeconfig secret for shoot %s: %w", obj.GetName(), err)
			}

			restConfig, err := clientcmd.RESTConfigFromKubeConfig(secret.Data["kubeconfig"])
			if err != nil {
				return nil, time.Time{}, fmt.Errorf("failed to get REST config from kubeconfig: %w", err)
			}

			renewTime, err := time.Parse(time.RFC3339, secret.Annotations[resourcesv1alpha1.ServiceAccountTokenRenewTimestamp])
			if err != nil {
				return nil, time.Time{}, fmt.Errorf("could not parse renew timestamp: %w", err)
			}

			return restConfig, renewTime.Add(5 * time.Minute), nil
		}
		p.checkReadiness = func(ctx context.Context, cli client.Client, object client.Object) (bool, error) {
			c, ok := object.(*extensionsv1alpha1.Cluster)
			if !ok {
				return false, fmt.Errorf("expected *extensionsv1alpha1.Cluster but got %T", object)
			}

			if err := cli.Get(ctx, client.ObjectKey{Name: secretName, Namespace: c.Name}, &corev1.Secret{}); err != nil {
				if !apierrors.IsNotFound(err) {
					return false, fmt.Errorf("failed reading kubeconfig secret for cluster %s: %w", c.Name, err)
				}
				return false, nil
			}

			return true, nil
		}

	default:
		return nil, fmt.Errorf("unknown topology %q", opts.Topology)
	}

	if err := builder.ControllerManagedBy(localMgr).
		For(p.newObject()).
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

// Provider is a cluster Provider that works with Gardener.
type Provider struct {
	opts   Options
	log    logr.Logger
	client client.Client

	lock      sync.Mutex
	mcMgr     mcmanager.Manager
	clusters  map[string]shoot
	cancelFns map[string]context.CancelFunc
	indexers  []index

	newObject      func() client.Object
	getKubeconfig  func(context.Context, client.Object) (*rest.Config, time.Time, error)
	checkReadiness func(context.Context, client.Client, client.Object) (bool, error)
}

type shoot struct {
	cluster             cluster.Cluster
	renewKubeconfigTime time.Time
}

// Get returns the cluster with the given name, if it is known.
func (p *Provider) Get(_ context.Context, clusterName string) (cluster.Cluster, error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	if cl, ok := p.clusters[clusterName]; ok {
		return cl.cluster, nil
	}

	return nil, fmt.Errorf("cluster %s not found", clusterName)
}

// Run starts the provider and blocks.
func (p *Provider) Run(ctx context.Context, mgr mcmanager.Manager) error {
	p.log.Info("Starting Gardener shoot provider")

	p.lock.Lock()
	p.mcMgr = mgr
	p.lock.Unlock()

	<-ctx.Done()

	return ctx.Err()
}

func (p *Provider) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := p.log.WithValues("namespace", req.Namespace, "name", req.Name)
	log.Info("Reconciling Shoot")

	key := req.NamespacedName.String()

	// get the object
	obj := p.newObject()
	if err := p.client.Get(ctx, req.NamespacedName, obj); err != nil {
		if apierrors.IsNotFound(err) {
			log.Error(err, "failed to get object")

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

	// provider already started?
	if p.mcMgr == nil {
		return reconcile.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// already engaged?
	if s, ok := p.clusters[key]; ok {
		if !time.Now().UTC().After(s.renewKubeconfigTime) {
			log.Info("Cluster already engaged, no need to refresh", "renewKubeconfigTime", s.renewKubeconfigTime)
			return reconcile.Result{}, nil
		}

		log.Info("Cluster already engaged, but need to refresh client - canceling it and requeueing")

		delete(p.clusters, key)
		if cancel, ok := p.cancelFns[key]; ok {
			cancel()
		}

		return reconcile.Result{Requeue: true}, nil
	}

	// ready and provisioned?
	if ready, err := p.checkReadiness(ctx, p.client, obj); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to check if cluster is ready: %w", err)
	} else if !ready {
		log.Info("Shoot is not ready yet")
		return reconcile.Result{}, nil
	}

	// get kubeconfig.
	cfg, renewKubeconfigTime, err := p.getKubeconfig(ctx, obj)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get kubeconfig: %w", err)
	}

	// create cluster.
	cl, err := cluster.New(cfg, p.opts.ClusterOptions...)
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
	p.clusters[key] = shoot{cl, renewKubeconfigTime}
	p.cancelFns[key] = cancel

	p.log.Info("Added new cluster", "cluster", key)

	// engage manager.
	if err := p.mcMgr.Engage(clusterCtx, key, cl); err != nil {
		log.Error(err, "failed to engage manager")
		delete(p.clusters, key)
		delete(p.cancelFns, key)
		return reconcile.Result{}, err
	}

	requeueAfter := renewKubeconfigTime.UTC().Sub(time.Now().UTC())
	log.Info("Scheduling client refresh", "requeueAfter", requeueAfter)
	return reconcile.Result{RequeueAfter: requeueAfter}, nil
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
	for key, shoot := range p.clusters {
		if err := shoot.cluster.GetCache().IndexField(ctx, obj, field, extractValue); err != nil {
			return fmt.Errorf("failed to index field %q on shoot %q: %w", field, key, err)
		}
	}

	return nil
}
