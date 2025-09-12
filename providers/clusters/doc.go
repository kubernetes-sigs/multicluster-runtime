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

// clusters implements a minimal multicluster provider.
//
// The provider is intended for use in test cases to easily add and
// remove [cluster.Cluster] to and from a multicluster-runtime manager
// to test operators that use multicluster-runtime.
// See provider_test.go for an example of how to use it in tests to
// engagen and disengage clusters.
//
// For embedding it is preferred that [clusters.Clusters] is embedded
// directly rather than embedding this provider.
//
// [cluster.Cluster]: sigs.k8s.io/controller-runtime/pkg/cluster
// [clusters.Clusters]: sigs.k8s.io/multicluster-runtime/pkg/clusters
package clusters
