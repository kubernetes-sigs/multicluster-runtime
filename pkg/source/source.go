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
	"sigs.k8s.io/controller-runtime/pkg/source"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// Source is a source of events (e.g. Create, Update, Delete operations on Kubernetes Objects, Webhook callbacks, etc)
// which should be processed by event.EventHandlers to enqueue reconcile.Requests.
//
// * Use Kind for events originating in the cluster (e.g. Pod Create, Pod Update, Deployment Update).
//
// * Use Channel for events originating outside the cluster (e.g. GitHub Webhook callback, Polling external urls).
//
// Users may build their own Source implementations.
type Source = TypedSource[client.Object, mcreconcile.Request]

// TypedSource is a generic source of events (e.g. Create, Update, Delete operations on Kubernetes Objects, Webhook callbacks, etc)
// which should be processed by event.EventHandlers to enqueue a request.
//
// * Use Kind for events originating in the cluster (e.g. Pod Create, Pod Update, Deployment Update).
//
// * Use Channel for events originating outside the cluster (e.g. GitHub Webhook callback, Polling external urls).
//
// Users may build their own Source implementations.
type TypedSource[object client.Object, request mcreconcile.ClusterAware[request]] interface {
	ForCluster(string, cluster.Cluster) (source.TypedSource[request], bool, error)
}

// SyncingSource is a source that needs syncing prior to being usable. The controller
// will call its WaitForSync prior to starting workers.
type SyncingSource[object client.Object] TypedSyncingSource[object, mcreconcile.Request]

// TypedSyncingSource is a source that needs syncing prior to being usable. The controller
// will call its WaitForSync prior to starting workers.
type TypedSyncingSource[object client.Object, request mcreconcile.ClusterAware[request]] interface {
	TypedSource[object, request]
	SyncingForCluster(string, cluster.Cluster) (source.TypedSyncingSource[request], bool, error)
	WithProjection(func(cluster.Cluster, object) (object, error)) TypedSyncingSource[object, request]
	WithClusterFilter(ClusterFilterFunc) TypedSyncingSource[object, request]
}
