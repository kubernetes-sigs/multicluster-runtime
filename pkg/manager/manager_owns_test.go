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

package manager

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	_ "github.com/onsi/ginkgo/v2"
)

type stubCluster struct{ cluster.Cluster }

type stubProvider struct {
	clusters map[multicluster.ClusterName]cluster.Cluster
}

func (p *stubProvider) Get(_ context.Context, name multicluster.ClusterName) (cluster.Cluster, error) {
	if cl, ok := p.clusters[name]; ok {
		return cl, nil
	}
	return nil, multicluster.ErrClusterNotFound
}

func (p *stubProvider) IndexField(context.Context, client.Object, string, client.IndexerFunc) error {
	return nil
}

type stubCoordinator struct {
	owned map[multicluster.ClusterName]bool
}

func (c *stubCoordinator) AddAware(multicluster.Aware) {}
func (c *stubCoordinator) Engage(context.Context, multicluster.ClusterName, cluster.Cluster) error {
	return nil
}
func (c *stubCoordinator) Runnable() manager.Runnable              { return nil }
func (c *stubCoordinator) Owns(name multicluster.ClusterName) bool { return c.owned[name] }

// A webhook or a read of another cluster needs a client for a cluster this
// process does not own, so GetCluster stays open while Owns says no.
func TestGetClusterReturnsAClusterThisProcessDoesNotOwn(t *testing.T) {
	notOwned := &stubCluster{}
	m := &mcManager{
		provider: &stubProvider{clusters: map[multicluster.ClusterName]cluster.Cluster{
			"owned": &stubCluster{}, "not-owned": notOwned,
		}},
		coord: &stubCoordinator{owned: map[multicluster.ClusterName]bool{"owned": true}},
	}

	got, err := m.GetCluster(t.Context(), "not-owned")
	if err != nil {
		t.Fatalf("expected the cluster, got error: %v", err)
	}
	if got != notOwned {
		t.Fatalf("expected the provider's cluster, got %v", got)
	}
	if m.Owns("not-owned") {
		t.Fatalf("expected Owns to be false for a cluster the coordinator does not own")
	}
	if !m.Owns("owned") {
		t.Fatalf("expected Owns to be true for a cluster the coordinator owns")
	}
	if !m.Owns(LocalCluster) {
		t.Fatalf("expected the local cluster to be owned")
	}
}

func TestOwnsWithoutCoordinator(t *testing.T) {
	m := &mcManager{provider: &stubProvider{}}
	if !m.Owns("any") {
		t.Fatalf("expected every cluster to be owned when no coordinator is configured")
	}
}
