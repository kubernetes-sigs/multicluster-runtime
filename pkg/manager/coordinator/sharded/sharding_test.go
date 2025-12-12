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
	"testing"
	"time"

	"github.com/go-logr/logr"

	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/multicluster-runtime/pkg/manager/coordinator/sharded/sharder"
)

type fakeRegistry struct{ p sharder.PeerInfo }

func (f *fakeRegistry) Self() sharder.PeerInfo        { return f.p }
func (f *fakeRegistry) Snapshot() []sharder.PeerInfo  { return []sharder.PeerInfo{f.p} }
func (f *fakeRegistry) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func TestOptions_ApplyPeerRegistry(t *testing.T) {
	s := runtime.NewScheme()
	_ = coordinationv1.AddToScheme(s)
	cli := fake.NewClientBuilder().WithScheme(s).Build()

	reg := &fakeRegistry{p: sharder.PeerInfo{ID: "x", Weight: 7}}
	c := New(cli, logr.Discard())

	WithPeerRegistry(reg)(c)
	if c.peers != reg {
		t.Fatalf("expected custom registry applied")
	}
	if c.self != reg.Self() {
		t.Fatalf("expected self to be updated from registry")
	}
}

func TestOptions_ApplyLeaseAndTimings(t *testing.T) {
	c := New(nil, logr.Discard())

	WithShardLease("ns", "name")(c)
	WithPerClusterLease(true)(c)
	WithLeaseTimings(30*time.Second, 10*time.Second, 750*time.Millisecond)(c)
	WithSynchronizationIntervals(5*time.Second, 15*time.Second)(c)

	cfg := c.cfg
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
