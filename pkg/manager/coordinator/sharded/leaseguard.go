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
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// leaseGuard manages a single Lease as a *fence* for one shard/cluster.
//
// Design/semantics:
//
//   - TryAcquire(ctx) attempts to create/adopt the Lease for holder `id`,
//     and, on success, starts a background renew loop. It is idempotent:
//     calling it while already held is a cheap true.
//
//   - Renewing: a ticker renews the Lease every `renew`. If renewal fails
//     (API error, conflict, or we observe another holder while still valid),
//     `onLost` is invoked once (best-effort), the fence is released, and the
//     loop exits.
//
//   - Release(ctx) stops renewing and, if we still own the Lease, clears
//     HolderIdentity (best-effort). Safe to call multiple times.
//
//   - Thread-safety: leaseGuard is intended to be used by a single goroutine
//     (caller) per fence. External synchronization is required if multiple
//     goroutines might call its methods concurrently.
//
//   - Timings: choose ldur (duration) > renew. A common pattern is
//     renew ≈ ldur/3. Too small ldur increases churn; too large slows
//     failover.
//
// RBAC: the caller’s client must be allowed to get/list/watch/create/update/patch
// Leases in namespace `ns`.
type leaseGuard struct {
	c      client.Client
	ns, nm string
	id     string
	ldur   time.Duration // lease duration
	renew  time.Duration // renew period
	onLost func()        // callback when we lose the lease

	held   bool
	cancel context.CancelFunc
}

// newLeaseGuard builds a guard; it does not contact the API server.
func newLeaseGuard(c client.Client, ns, name, id string, ldur, renew time.Duration, onLost func()) *leaseGuard {
	return &leaseGuard{c: c, ns: ns, nm: name, id: id, ldur: ldur, renew: renew, onLost: onLost}
}

// TryAcquire creates/adopts the Lease for g.id and starts renewing it.
// Returns true iff we own it after this call (or already owned).
// Fails (returns false) when another non-expired holder exists or API calls error.
func (g *leaseGuard) TryAcquire(ctx context.Context) bool {
	if g.held {
		return true
	}

	key := types.NamespacedName{Namespace: g.ns, Name: g.nm}
	now := metav1.NowMicro()

	ldurSec := int32(g.ldur / time.Second)

	var ls coordinationv1.Lease
	err := g.c.Get(ctx, key, &ls)
	switch {
	case apierrors.IsNotFound(err):
		ls = coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Namespace: g.ns, Name: g.nm},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &g.id,
				LeaseDurationSeconds: &ldurSec,
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}
		if err := g.c.Create(ctx, &ls); err != nil {
			return false
		}
	case err != nil:
		return false
	default:
		// adopt if free/expired/ours
		ho := ""
		if ls.Spec.HolderIdentity != nil {
			ho = *ls.Spec.HolderIdentity
		}
		if ho != "" && ho != g.id {
			if !expired(&ls, now) {
				return false
			}
		}
		ls.Spec.HolderIdentity = &g.id
		ls.Spec.LeaseDurationSeconds = &ldurSec
		// keep first AcquireTime if already ours, otherwise set it
		if ho != g.id || ls.Spec.AcquireTime == nil {
			ls.Spec.AcquireTime = &now
		}
		ls.Spec.RenewTime = &now
		if err := g.c.Update(ctx, &ls); err != nil {
			return false
		}
	}

	// we own it; start renewer
	rctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.held = true
	go g.renewLoop(rctx, key)
	return true
}

// Internal: renew loop and single-step renew. If renewal observes a different,
// valid holder or API errors persist, onLost() is invoked once and the fence is released.
func (g *leaseGuard) renewLoop(ctx context.Context, key types.NamespacedName) {
	t := time.NewTicker(g.renew)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if ok := g.renewOnce(ctx, key); !ok {
				// best-effort notify once, then release
				if g.onLost != nil {
					g.onLost()
				}
				g.Release(context.Background())
				return
			}
		}
	}
}

func (g *leaseGuard) renewOnce(ctx context.Context, key types.NamespacedName) bool {
	now := metav1.NowMicro()
	var ls coordinationv1.Lease
	if err := g.c.Get(ctx, key, &ls); err != nil {
		return false
	}
	// another holder?
	if ls.Spec.HolderIdentity != nil && *ls.Spec.HolderIdentity != g.id && !expired(&ls, now) {
		return false
	}
	ldurSec := int32(g.ldur / time.Second)
	// update
	ls.Spec.HolderIdentity = &g.id
	ls.Spec.LeaseDurationSeconds = &ldurSec
	ls.Spec.RenewTime = &now
	if err := g.c.Update(ctx, &ls); err != nil {
		return false
	}
	return true
}

// Release stops renewing; best-effort clear if we still own it.
func (g *leaseGuard) Release(ctx context.Context) {
	if !g.held {
		return
	}
	if g.cancel != nil {
		g.cancel()
	}
	g.held = false

	key := types.NamespacedName{Namespace: g.ns, Name: g.nm}
	var ls coordinationv1.Lease
	if err := g.c.Get(ctx, key, &ls); err == nil {
		if ls.Spec.HolderIdentity != nil && *ls.Spec.HolderIdentity == g.id {
			empty := ""
			ls.Spec.HolderIdentity = &empty
			// keep RenewTime/AcquireTime; just clear holder
			_ = g.c.Update(ctx, &ls) // ignore errors
		}
	}
}

func expired(ls *coordinationv1.Lease, now metav1.MicroTime) bool {
	if ls.Spec.RenewTime == nil || ls.Spec.LeaseDurationSeconds == nil {
		return true
	}
	return now.Time.After(ls.Spec.RenewTime.Time.Add(time.Duration(*ls.Spec.LeaseDurationSeconds) * time.Second))
}
