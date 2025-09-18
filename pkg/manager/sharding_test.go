package manager

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"

	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/multicluster-runtime/pkg/manager/sharder"
)

type fakeRegistry struct{ p sharder.PeerInfo }

func (f *fakeRegistry) Self() sharder.PeerInfo        { return f.p }
func (f *fakeRegistry) Snapshot() []sharder.PeerInfo  { return []sharder.PeerInfo{f.p} }
func (f *fakeRegistry) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func TestOptions_ApplyPeerRegistry(t *testing.T) {
	s := runtime.NewScheme()
	_ = coordinationv1.AddToScheme(s)
	cli := fake.NewClientBuilder().WithScheme(s).Build()

	cfg := SynchronizationConfig{}
	reg := &fakeRegistry{p: sharder.PeerInfo{ID: "x", Weight: 7}}
	eng := newSynchronizationEngine(cli, logr.Discard(), sharder.NewHRW(), reg, reg.Self(), cfg)
	m := &mcManager{Manager: nil, provider: nil, engine: eng}

	WithPeerRegistry(reg)(m)
	if m.engine.peers != reg {
		t.Fatalf("expected custom registry applied")
	}
	if m.engine.self != reg.Self() {
		t.Fatalf("expected self to be updated from registry")
	}
}

func TestOptions_ApplyLeaseAndTimings(t *testing.T) {
	m := &mcManager{engine: &synchronizationEngine{cfg: SynchronizationConfig{}}}
	WithShardLease("ns", "name")(m)
	WithPerClusterLease(true)(m)
	WithLeaseTimings(30*time.Second, 10*time.Second, 750*time.Millisecond)(m)
	WithSynchronizationIntervals(5*time.Second, 15*time.Second)(m)

	cfg := m.engine.cfg
	if cfg.FenceNS != "ns" || cfg.FencePrefix != "name" || !cfg.PerClusterLease {
		t.Fatalf("lease cfg not applied: %+v", cfg)
	}
	if cfg.LeaseDuration != 30*time.Second || cfg.LeaseRenew != 10*time.Second || cfg.FenceThrottle != 750*time.Millisecond {
		t.Fatalf("timings not applied: %+v", cfg)
	}
	if cfg.Probe != 5*time.Second || cfg.Rehash != 15*time.Second {
		t.Fatalf("cadence not applied: %+v", cfg)
	}
}
