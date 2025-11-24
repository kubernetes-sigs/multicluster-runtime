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

package source

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// Kind creates a KindSource with the given cache provider.
func Kind[object client.Object](
	obj object,
	handler mchandler.TypedEventHandlerFunc[object, mcreconcile.Request],
	predicates ...predicate.TypedPredicate[object],
) SyncingSource[object] {
	return TypedKind[object, mcreconcile.Request](obj, handler, predicates...)
}

// TypedKind creates a KindSource with the given cache provider.
func TypedKind[object client.Object, request mcreconcile.ClusterAware[request]](
	obj object,
	handler mchandler.TypedEventHandlerFunc[object, request],
	predicates ...predicate.TypedPredicate[object],
) TypedSyncingSource[object, request] {
	return &kind[object, request]{
		obj:        obj,
		handler:    handler,
		predicates: predicates,
		project:    func(_ cluster.Cluster, obj object) (object, error) { return obj, nil },
	}
}

type kind[object client.Object, request mcreconcile.ClusterAware[request]] struct {
	obj        object
	handler    mchandler.TypedEventHandlerFunc[object, request]
	predicates []predicate.TypedPredicate[object]
	project    func(cluster.Cluster, object) (object, error)
}

type clusterKind[object client.Object, request mcreconcile.ClusterAware[request]] struct {
	source.TypedSyncingSource[request]
}

// WithProjection sets the projection function for the KindSource.
func (k *kind[object, request]) WithProjection(project func(cluster.Cluster, object) (object, error)) TypedSyncingSource[object, request] {
	k.project = project
	return k
}

func (k *kind[object, request]) ForCluster(name string, cl cluster.Cluster) (source.TypedSource[request], error) {
	obj, err := k.project(cl, k.obj)
	if err != nil {
		return nil, err
	}
	return &clusterKind[object, request]{
		TypedSyncingSource: source.TypedKind(cl.GetCache(), obj, k.handler(name, cl), k.predicates...),
	}, nil
}

func (k *kind[object, request]) SyncingForCluster(name string, cl cluster.Cluster) (source.TypedSyncingSource[request], error) {
	obj, err := k.project(cl, k.obj)
	if err != nil {
		return nil, err
	}
	return &clusterKind[object, request]{
		TypedSyncingSource: source.TypedKind(cl.GetCache(), obj, k.handler(name, cl), k.predicates...),
	}, nil
}
