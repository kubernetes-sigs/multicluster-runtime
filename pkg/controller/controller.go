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

package controller

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/client-go/util/workqueue"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	mcsource "sigs.k8s.io/multicluster-runtime/pkg/source"
)

// Controller implements a Kubernetes API.  A Controller manages a work queue fed reconcile.Requests
// from source.Sources.  Work is performed through the reconcile.Reconciler for each enqueued item.
// Work typically is reads and writes Kubernetes objects to make the system state match the state specified
// in the object Spec.
type Controller = TypedController[mcreconcile.Request]

// Options are the arguments for creating a new Controller.
type Options = controller.TypedOptions[mcreconcile.Request]

// TypedController implements an API.
type TypedController[request mcreconcile.ClusterAware[request]] interface {
	controller.TypedController[request]
	multicluster.Aware

	// MultiClusterWatch watches the provided Source.
	MultiClusterWatch(src mcsource.TypedSource[client.Object, request]) error
}

// New returns a new Controller registered with the Manager.  The Manager will ensure that shared Caches have
// been synced before the Controller is Started.
//
// The name must be unique as it is used to identify the controller in metrics and logs.
func New(name string, mgr mcmanager.Manager, options Options) (Controller, error) {
	return NewTyped(name, mgr, options)
}

// NewTyped returns a new typed controller registered with the Manager,
//
// The name must be unique as it is used to identify the controller in metrics and logs.
func NewTyped[request mcreconcile.ClusterAware[request]](name string, mgr mcmanager.Manager, options controller.TypedOptions[request]) (TypedController[request], error) {
	c, err := NewTypedUnmanaged(name, mgr, options)
	if err != nil {
		return nil, err
	}

	// Add the controller as a Manager components
	return c, mgr.Add(c)
}

// NewUnmanaged returns a new controller without adding it to the manager. The
// caller is responsible for starting the returned controller.
//
// The name must be unique as it is used to identify the controller in metrics and logs.
func NewUnmanaged(name string, mgr mcmanager.Manager, options Options) (Controller, error) {
	return NewTypedUnmanaged[mcreconcile.Request](name, mgr, options)
}

// NewTypedUnmanaged returns a new typed controller without adding it to the manager.
//
// The name must be unique as it is used to identify the controller in metrics and logs.
func NewTypedUnmanaged[request mcreconcile.ClusterAware[request]](name string, mgr mcmanager.Manager, options controller.TypedOptions[request]) (TypedController[request], error) {
	c, err := controller.NewTypedUnmanaged[request](name, options)
	if err != nil {
		return nil, err
	}
	return &mcController[request]{
		TypedController: c,
		clusters:        make(map[string]engagedCluster),
	}, nil
}

var _ TypedController[mcreconcile.Request] = &mcController[mcreconcile.Request]{}

type mcController[request mcreconcile.ClusterAware[request]] struct {
	controller.TypedController[request]

	lock     sync.Mutex
	clusters map[string]engagedCluster
	sources  []mcsource.TypedSource[client.Object, request]
}

type engagedCluster struct {
	name    string
	cluster cluster.Cluster
}

func (c *mcController[request]) Engage(ctx context.Context, name string, cl cluster.Cluster) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	if old, ok := c.clusters[name]; ok && old.cluster == cl {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx) //nolint:govet // cancel is called in the error case only.

	// pass through in case the controller itself is cluster aware
	if ctrl, ok := c.TypedController.(multicluster.Aware); ok {
		if err := ctrl.Engage(ctx, name, cl); err != nil {
			cancel()
			return err
		}
	}

	// engage cluster aware instances
	for _, aware := range c.sources {
		src, err := aware.ForCluster(name, cl)
		if err != nil {
			cancel()
			return fmt.Errorf("failed to engage for cluster %q: %w", name, err)
		}
		if err := c.TypedController.Watch(startWithinContext[request](ctx, src)); err != nil {
			cancel()
			return fmt.Errorf("failed to watch for cluster %q: %w", name, err)
		}
	}

	ec := engagedCluster{
		name:    name,
		cluster: cl,
	}
	c.clusters[name] = ec
	go func() {
		c.lock.Lock()
		defer c.lock.Unlock()
		if c.clusters[name] == ec {
			delete(c.clusters, name)
		}
	}()

	return nil //nolint:govet // cancel is called in the error case only.
}

func (c *mcController[request]) MultiClusterWatch(src mcsource.TypedSource[client.Object, request]) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	ctx, cancel := context.WithCancel(context.Background()) //nolint:govet // cancel is called in the error case only.

	for name, eng := range c.clusters {
		src, err := src.ForCluster(name, eng.cluster)
		if err != nil {
			cancel()
			return fmt.Errorf("failed to engage for cluster %q: %w", name, err)
		}
		if err := c.TypedController.Watch(startWithinContext[request](ctx, src)); err != nil {
			cancel()
			return fmt.Errorf("failed to watch for cluster %q: %w", name, err)
		}
	}

	c.sources = append(c.sources, src)

	return nil //nolint:govet // cancel is called in the error case only.
}

func startWithinContext[request mcreconcile.ClusterAware[request]](ctx context.Context, src source.TypedSource[request]) source.TypedSource[request] {
	return source.TypedFunc[request](func(ctlCtx context.Context, w workqueue.TypedRateLimitingInterface[request]) error {
		ctx, cancel := context.WithCancel(ctx)
		go func() {
			<-ctlCtx.Done()
			cancel()
		}()
		return src.Start(ctx, w)
	})
}
