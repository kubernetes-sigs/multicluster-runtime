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

package handler

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// EnqueueRequestsFromMapFunc wraps handler.EnqueueRequestsFromMapFunc to be
// compatible with multi-cluster.
func EnqueueRequestsFromMapFunc(fn handler.MapFunc) EventHandlerFunc {
	return WithCluster(handler.EnqueueRequestsFromMapFunc(fn))
}

// TypedEnqueueRequestsFromMapFunc wraps handler.TypedEnqueueRequestsFromMapFunc
// to be compatible with multi-cluster.
func TypedEnqueueRequestsFromMapFunc[object client.Object, request comparable](fn handler.TypedMapFunc[object, request]) TypedEventHandlerFunc[object, request] {
	return TypedWithCluster[object, request](handler.TypedEnqueueRequestsFromMapFunc[object, request](fn))
}
