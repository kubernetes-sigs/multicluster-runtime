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

package sharded

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-logr/logr"

	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	"sigs.k8s.io/multicluster-runtime/pkg/manager/coordinator/sharded/sharder"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

type stubSharder struct{ own atomic.Bool }

func (s *stubSharder) ShouldOwn(clusterID string, _ []sharder.PeerInfo, _ sharder.PeerInfo) bool {
	return s.own.Load()
}

type stubRegistry struct{ self sharder.PeerInfo }

func (r *stubRegistry) Self() sharder.PeerInfo        { return r.self }
func (r *stubRegistry) Snapshot() []sharder.PeerInfo  { return []sharder.PeerInfo{r.self} }
func (r *stubRegistry) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

type stubRunnable struct{ called chan multicluster.ClusterName }

func (s *stubRunnable) Engage(ctx context.Context, name multicluster.ClusterName, cl cluster.Cluster) error {
	select {
	case s.called <- name:
	default:
	}
	return nil
}

func TestCoordinator_StartsWhenShouldOwnAndFenceAcquired(t *testing.T) {
	s := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(s).Build()

	cfg := Config{
		FenceNS: "kube-system", FencePrefix: "mcr-shard", PerClusterLease: true,
		LeaseDuration: 3 * time.Second, LeaseRenew: 50 * time.Millisecond, FenceThrottle: 50 * time.Millisecond,
		PeerPrefix: "mcr-peer", PeerWeight: 1, Probe: 10 * time.Millisecond,
	}
	reg := &stubRegistry{self: sharder.PeerInfo{ID: "peer-0", Weight: 1}}
	sh := &stubSharder{}
	sh.own.Store(true)
	c := New(cli, logr.Discard(),
		withConfig(cfg),
		WithPeerRegistry(reg),
		WithSharder(sh),
	)

	sink := &stubRunnable{called: make(chan multicluster.ClusterName, 1)}
	c.AddAware(sink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := c.Engage(ctx, "zoo", nil); err != nil {
		t.Fatalf("engage: %v", err)
	}
	// Force a recompute to decide and start
	c.recompute(ctx)

	select {
	case name := <-sink.called:
		if name != "zoo" {
			t.Fatalf("expected engage for zoo, got %s", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected runnable to be engaged")
	}

	// Verify Lease created and held by self
	var ls coordinationv1.Lease
	key := client.ObjectKey{Namespace: cfg.FenceNS, Name: c.fenceName("zoo")}
	if err := cli.Get(ctx, key, &ls); err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if ls.Spec.HolderIdentity == nil || *ls.Spec.HolderIdentity != reg.self.ID {
		t.Fatalf("expected holder %q, got %+v", reg.self.ID, ls.Spec.HolderIdentity)
	}
}

func TestCoordinator_Owns(t *testing.T) {
	s := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(s).Build()

	cfg := Config{
		FenceNS: "kube-system", FencePrefix: "mcr-shard", PerClusterLease: true,
		LeaseDuration: 3 * time.Second, LeaseRenew: 50 * time.Millisecond, FenceThrottle: 50 * time.Millisecond,
		PeerPrefix: "mcr-peer", PeerWeight: 1, Probe: 10 * time.Millisecond,
	}
	reg := &stubRegistry{self: sharder.PeerInfo{ID: "peer-0", Weight: 1}}
	sh := &stubSharder{own: true}
	c := New(cli, logr.Discard(),
		withConfig(cfg),
		WithPeerRegistry(reg),
		WithSharder(sh),
	)
	c.AddAware(&stubRunnable{called: make(chan multicluster.ClusterName, 1)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if c.Owns("zoo") {
		t.Fatalf("expected Owns to be false before engagement")
	}

	if err := c.Engage(ctx, "zoo", nil); err != nil {
		t.Fatalf("engage: %v", err)
	}

	if c.Owns("zoo") {
		t.Fatalf("expected Owns to be false before ownership is decided and the fence acquired")
	}

	// Force a recompute to decide and start.
	c.recompute(ctx)

	if !c.Owns("zoo") {
		t.Fatalf("expected Owns to be true once the fence lease is acquired and runnables started")
	}
	if c.Owns("unknown-cluster") {
		t.Fatalf("expected Owns to be false for a cluster never engaged")
	}

	// Flip ownership to false and recompute; Owns should reflect the stop.
	sh.own = false
	c.recompute(ctx)

	if c.Owns("zoo") {
		t.Fatalf("expected Owns to be false after losing ownership")
	}
}

func TestCoordinator_StopsAndReleasesWhenShouldOwnFalse(t *testing.T) {
	s := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	cli := fake.NewClientBuilder().WithScheme(s).Build()

	cfg := Config{
		FenceNS: "kube-system", FencePrefix: "mcr-shard", PerClusterLease: true,
		LeaseDuration: 3 * time.Second, LeaseRenew: 50 * time.Millisecond, FenceThrottle: 50 * time.Millisecond,
		PeerPrefix: "mcr-peer", PeerWeight: 1, Probe: 10 * time.Millisecond,
	}
	reg := &stubRegistry{self: sharder.PeerInfo{ID: "peer-0", Weight: 1}}
	sh := &stubSharder{}
	sh.own.Store(true)
	c := New(cli, logr.Discard(),
		withConfig(cfg),
		WithPeerRegistry(reg),
		WithSharder(sh),
	)

	c.AddAware(&stubRunnable{called: make(chan multicluster.ClusterName, 1)})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := c.Engage(ctx, "zoo", nil); err != nil {
		t.Fatalf("engage: %v", err)
	}
	// start and acquire lease
	c.recompute(ctx)

	// Flip ownership to false and recompute; coordinator should stop and release fence
	sh.own.Store(false)
	c.recompute(ctx)

	// Poll for lease holder cleared by Release()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var ls coordinationv1.Lease
		if err := cli.Get(ctx, client.ObjectKey{Namespace: cfg.FenceNS, Name: c.fenceName("zoo")}, &ls); err == nil {
			if ls.Spec.HolderIdentity != nil && *ls.Spec.HolderIdentity == "" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected lease holder to be cleared after release")
}
