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

package peers

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"

	coordv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	_ "github.com/onsi/ginkgo/v2"
)

const (
	testNS  = "kube-system"
	prefix  = "mcr-peer"
	selfID  = "peer-0"
	otherID = "peer-1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := coordv1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
}

func getLease(t *testing.T, c crclient.Client, name string) *coordv1.Lease {
	t.Helper()
	var ls coordv1.Lease
	if err := c.Get(context.Background(), crclient.ObjectKey{Namespace: testNS, Name: name}, &ls); err != nil {
		t.Fatalf("get lease %s: %v", name, err)
	}
	return &ls
}

func TestRenewSelfLease_CreateThenUpdate(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	r := NewLeaseRegistry(c, testNS, prefix, selfID, 2, logr.Discard()).(*leaseRegistry)

	// First call creates the Lease
	if err := r.renewSelfLease(context.Background()); err != nil {
		t.Fatalf("renewSelfLease create: %v", err)
	}
	name := prefix + "-" + selfID
	ls := getLease(t, c, name)
	if ls.Spec.HolderIdentity == nil || *ls.Spec.HolderIdentity != selfID {
		t.Fatalf("holder = %v, want %q", ls.Spec.HolderIdentity, selfID)
	}
	if got := ls.Labels[labelPeer]; got != "true" {
		t.Fatalf("label %s = %q, want true", labelPeer, got)
	}
	if got := ls.Labels[labelPrefix]; got != prefix {
		t.Fatalf("label %s = %q, want %s", labelPrefix, got, prefix)
	}
	if got := ls.Annotations[annotWeight]; got != "2" {
		t.Fatalf("weight annot = %q, want 2", got)
	}

	// Update path: change weight and ensure it is written
	r.self.Weight = 3
	time.Sleep(1 * time.Millisecond) // ensure RenewTime advances
	if err := r.renewSelfLease(context.Background()); err != nil {
		t.Fatalf("renewSelfLease update: %v", err)
	}
	ls2 := getLease(t, c, name)
	if got := ls2.Annotations[annotWeight]; got != "3" {
		t.Fatalf("updated weight annot = %q, want 3", got)
	}
	if ls.Spec.RenewTime == nil || ls2.Spec.RenewTime == nil ||
		!ls2.Spec.RenewTime.Time.After(ls.Spec.RenewTime.Time) {
		t.Fatalf("RenewTime did not advance: old=%v new=%v", ls.Spec.RenewTime, ls2.Spec.RenewTime)
	}
}

func TestRefreshPeers_FiltersAndParses(t *testing.T) {
	s := newScheme(t)

	now := time.Now()
	validRenew := metav1.NewMicroTime(now)
	expiredRenew := metav1.NewMicroTime(now.Add(-time.Hour))
	dur := int32(60)

	// valid peer (otherID), correct labels/prefix, not expired, weight 5
	valid := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNS, Name: prefix + "-" + otherID,
			Labels: map[string]string{
				labelPartOf: partOfValue,
				labelPeer:   "true",
				labelPrefix: prefix,
			},
			Annotations: map[string]string{
				annotWeight: "5",
			},
		},
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       &[]string{otherID}[0],
			RenewTime:            &validRenew,
			LeaseDurationSeconds: &dur,
		},
	}

	// expired peer (should be ignored)
	expired := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNS, Name: prefix + "-expired",
			Labels: map[string]string{
				labelPeer:   "true",
				labelPrefix: prefix,
			},
		},
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       &[]string{"nobody"}[0],
			RenewTime:            &expiredRenew,
			LeaseDurationSeconds: &dur,
		},
	}

	// wrong prefix (should be ignored)
	wrong := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNS, Name: "otherprefix-" + "x",
			Labels: map[string]string{
				labelPeer:   "true",
				labelPrefix: "otherprefix",
			},
		},
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       &[]string{"x"}[0],
			RenewTime:            &validRenew,
			LeaseDurationSeconds: &dur,
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).WithObjects(valid, expired, wrong).Build()

	r := NewLeaseRegistry(c, testNS, prefix, selfID, 1, logr.Discard()).(*leaseRegistry)
	if err := r.refreshPeers(context.Background()); err != nil {
		t.Fatalf("refreshPeers: %v", err)
	}

	snap := r.Snapshot()

	// Expect self + valid other
	want := map[string]uint32{
		selfID:  1,
		otherID: 5,
	}
	got := map[string]uint32{}
	for _, p := range snap {
		got[p.ID] = p.Weight
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("snapshot missing/mismatch for %s: got %d want %d; full=%v", id, got[id], w, got)
		}
	}
	// Should not include expired/wrong
	if _, ok := got["expired"]; ok {
		t.Fatalf("snapshot unexpectedly contains expired")
	}
	if _, ok := got["x"]; ok {
		t.Fatalf("snapshot unexpectedly contains wrong prefix")
	}
}

func TestRun_PublishesSelfAndStopsOnCancel(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := NewLeaseRegistry(c, testNS, prefix, selfID, 1, logr.Discard()).(*leaseRegistry)

	// Speed up the loop for the test
	r.ttl = 2 * time.Second
	r.renew = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		_ = r.Run(ctx)
		close(done)
	}()

	// wait for at least one tick
	time.Sleep(120 * time.Millisecond)

	// self lease should exist
	name := prefix + "-" + selfID
	_ = getLease(t, c, name) // will fatal if missing

	// cancel and ensure Run returns
	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("Run did not exit after cancel")
	}
}
