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

package acceptance

import (
	"context"
	"crypto/rand"
	"testing"

	"github.com/google/go-cmp/cmp"

	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/cluster"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// ClusterGenerator is a function that generates a new cluster.
// The cluster is expected to be available through the provider.
// The return values are the cluster name, the rest.Config to access the
// cluster, and an error if the cluster could not be created.
//
// The context is cancelled when the cluster is to be removed.
//
// The ErrorHandler can be used to report errors from goroutines started
// by the generator.
type ClusterGenerator func(context.Context, ErrorHandler) (string, *rest.Config, error)

// UnknownClusterName is a random cluster name used to test the
// return of a correct error for non-existing clusters.
// Providers may use this in their test to verify their generated
// names do not accidentally collide with this name.
var UnknownClusterName = rand.Text()

// RandomClusterName generates a random cluster name that is not
// UnknownClusterName.
func RandomClusterName() string {
	name := rand.Text()
	if name == UnknownClusterName {
		return RandomClusterName()
	}
	return name
}

func createCluster(t testing.TB, clusterGenerator ClusterGenerator) (string, *rest.Config) {
	t.Helper()
	clusterName, clusterCfg, err := clusterGenerator(t.Context(), errorHandler(t, "cluster generator"))
	if err != nil {
		t.Fatalf("Failed to create cluster: %v", err)
	}
	return clusterName, clusterCfg
}

func getCluster(t testing.TB, manager mcmanager.Manager, clusterName string, clusterCfg *rest.Config) cluster.Cluster {
	t.Helper()
	t.Logf("Retrieving cluster %q", clusterName)

	var cl cluster.Cluster
	eventually(t, func() error {
		var err error
		cl, err = manager.GetCluster(t.Context(), clusterName)
		return err
	}, WaitTimeout, PollInterval, "cluster %q not found", clusterName)

	cfg := rest.CopyConfig(cl.GetConfig())
	// These values are not persisted in the kubeconfig
	cfg.QPS = clusterCfg.QPS
	cfg.Burst = clusterCfg.Burst

	if diff := cmp.Diff(clusterCfg, cfg); diff != "" {
		t.Errorf("Cluster config mismatch: %s", diff)
	}

	return cl
}
