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

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// test constants
const (
	testNS   = "kube-system"
	testName = "mcr-shard-default"
	testID   = "peer-0"
	otherID  = "peer-1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
}

func getLease(t *testing.T, c crclient.Client) *coordinationv1.Lease {
	t.Helper()
	var ls coordinationv1.Lease
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: testName}, &ls); err != nil {
		t.Fatalf("get lease: %v", err)
	}
	return &ls
}

func makeLease(holder string, renewAgo time.Duration, durSec int32) *coordinationv1.Lease {
	now := time.Now()
	renew := metav1.NewMicroTime(now.Add(-renewAgo))
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: testName},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &durSec,
			RenewTime:            &renew,
			AcquireTime:          &renew,
		},
	}
}

func TestTryAcquire_CreatesAndRenewsThenReleaseClearsHolder(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	onLostCh := make(chan struct{}, 1)
	g := newLeaseGuard(c, testNS, testName, testID, 3*time.Second, 200*time.Millisecond, func() { onLostCh <- struct{}{} })

	if ok := g.TryAcquire(context.Background()); !ok {
		t.Fatalf("expected TryAcquire to succeed on create")
	}
	if !g.held {
		t.Fatalf("guard should be held after TryAcquire")
	}

	ls := getLease(t, c)
	if ls.Spec.HolderIdentity == nil || *ls.Spec.HolderIdentity != testID {
		t.Fatalf("holder mismatch, got %v", ls.Spec.HolderIdentity)
	}
	if ls.Spec.LeaseDurationSeconds == nil || *ls.Spec.LeaseDurationSeconds != int32(3) {
		t.Fatalf("duration mismatch, got %v", ls.Spec.LeaseDurationSeconds)
	}

	// Idempotent
	if ok := g.TryAcquire(context.Background()); !ok {
		t.Fatalf("expected idempotent TryAcquire to return true")
	}

	// Release clears holder (best-effort)
	g.Release(context.Background())
	ls = getLease(t, c)
	if ls.Spec.HolderIdentity != nil && *ls.Spec.HolderIdentity != "" {
		t.Fatalf("expected holder cleared on release, got %q", *ls.Spec.HolderIdentity)
	}
}

func TestTryAcquire_FailsWhenOtherHoldsAndNotExpired(t *testing.T) {
	s := newScheme(t)
	// Other holder renewed recently, not expired
	dur := int32(30)
	ls := makeLease(otherID, 1*time.Second, dur)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ls).Build()

	g := newLeaseGuard(c, testNS, testName, testID, 10*time.Second, 2*time.Second, nil)

	if ok := g.TryAcquire(context.Background()); ok {
		t.Fatalf("expected TryAcquire to fail while another valid holder exists")
	}
	if g.held {
		t.Fatalf("guard should not be held")
	}
}

func TestTryAcquire_AdoptsWhenExpired(t *testing.T) {
	s := newScheme(t)
	// Other holder expired: renew time far in the past relative to duration
	dur := int32(5)
	ls := makeLease(otherID, 30*time.Second, dur)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ls).Build()

	g := newLeaseGuard(c, testNS, testName, testID, 10*time.Second, 2*time.Second, nil)

	if ok := g.TryAcquire(context.Background()); !ok {
		t.Fatalf("expected TryAcquire to adopt expired lease")
	}
	got := getLease(t, c)
	if got.Spec.HolderIdentity == nil || *got.Spec.HolderIdentity != testID {
		t.Fatalf("expected holder=%q, got %v", testID, got.Spec.HolderIdentity)
	}
}

func TestRenewLoop_OnLostTriggersCallbackAndReleases(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	lost := make(chan struct{}, 1)
	g := newLeaseGuard(c, testNS, testName, testID, 3*time.Second, 50*time.Millisecond, func() { lost <- struct{}{} })

	// Acquire
	if ok := g.TryAcquire(context.Background()); !ok {
		t.Fatalf("expected TryAcquire to succeed")
	}

	// Flip lease to another holder (valid) so renewOnce observes loss.
	ls := getLease(t, c)
	now := metav1.NowMicro()
	dur := int32(3)
	other := otherID
	ls.Spec.HolderIdentity = &other
	ls.Spec.LeaseDurationSeconds = &dur
	ls.Spec.RenewTime = &now
	if err := c.Update(context.Background(), ls); err != nil {
		t.Fatalf("update lease: %v", err)
	}

	// Expect onLost to fire and guard to release within a few renew ticks
	select {
	case <-lost:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatalf("expected onLost to be called after renewal detects loss")
	}

	// Give a moment for Release() to run
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := getLease(t, c)
		if got.Spec.HolderIdentity == nil || *got.Spec.HolderIdentity == "" || *got.Spec.HolderIdentity != testID {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected holder to be cleared or moved after loss")
}

func TestRelease_NoHoldIsNoop(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	g := newLeaseGuard(c, testNS, testName, testID, 3*time.Second, 1*time.Second, nil)
	// Not acquired yet; should be a no-op
	g.Release(context.Background())
	// Nothing to assert other than "does not panic"
}
