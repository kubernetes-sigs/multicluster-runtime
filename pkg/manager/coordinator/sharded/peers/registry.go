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
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-logr/logr"

	coordv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/multicluster-runtime/pkg/manager/coordinator/sharded/sharder"
	"sigs.k8s.io/multicluster-runtime/pkg/util/sanitize"
)

const (
	labelPartOf   = "app.kubernetes.io/part-of"
	labelPeer     = "mcr.sigs.k8s.io/peer"
	labelPrefix   = "mcr.sigs.k8s.io/prefix"
	annotWeight   = "mcr.sigs.k8s.io/weight"
	partOfValue   = "multicluster-runtime"
	defaultWeight = uint32(1)
)

// Registry provides peer membership for sharding decisions.
type Registry interface {
	// Self returns this process's identity and weight.
	Self() sharder.PeerInfo
	// Snapshot returns the current set of live peers. Treat as read-only.
	Snapshot() []sharder.PeerInfo
	// Run blocks, periodically renewing our Lease and refreshing peer membership
	// until ctx is cancelled or an error occurs.
	Run(ctx context.Context) error
}

type leaseRegistry struct {
	ns, namePrefix string
	self           sharder.PeerInfo
	cli            crclient.Client

	mu    sync.RWMutex
	peers map[string]sharder.PeerInfo

	ttl   time.Duration
	renew time.Duration

	log logr.Logger
}

// NewLeaseRegistry constructs a Lease-based Registry.
//
// Params:
//   - ns: namespace where peer Leases live (e.g. "kube-system")
//   - namePrefix: Lease name prefix (e.g. "mcr-peer")
//   - selfID: this process ID (defaults to os.Hostname() if empty)
//   - weight: relative capacity (0 treated as 1)
//   - log: logger; use logr.Discard() to silence
func NewLeaseRegistry(cli crclient.Client, ns, namePrefix string, selfID string, weight uint32, log logr.Logger) Registry {
	if selfID == "" {
		if hn, _ := os.Hostname(); hn != "" {
			selfID = hn
		} else {
			selfID = "unknown"
		}
	}
	// Sanitize to DNS-1123 subdomain for Lease names: lowercase, [a-z0-9-.], start/end alphanumeric.
	selfID = sanitize.DNS1123(selfID)
	if weight == 0 {
		weight = defaultWeight
	}
	return &leaseRegistry{
		ns:         ns,
		namePrefix: namePrefix,
		self:       sharder.PeerInfo{ID: selfID, Weight: weight},
		cli:        cli,
		peers:      map[string]sharder.PeerInfo{},
		ttl:        30 * time.Second,
		renew:      10 * time.Second,
		log:        log.WithName("lease-registry").WithValues("ns", ns, "prefix", namePrefix, "selfID", selfID, "weight", weight),
	}
}

func (r *leaseRegistry) Self() sharder.PeerInfo { return r.self }

func (r *leaseRegistry) Snapshot() []sharder.PeerInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]sharder.PeerInfo, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, p)
	}
	return out
}

func (r *leaseRegistry) Run(ctx context.Context) error {
	r.log.Info("peer registry starting", "ns", r.ns, "prefix", r.namePrefix, "self", r.self.ID)

	// Tick frequently enough to renew well within ttl.
	t := time.NewTicker(r.renew)
	defer t.Stop()

	for {
		// Do one pass immediately so we publish our presence promptly.
		if err := r.renewSelfLease(ctx); err != nil && ctx.Err() == nil {
			r.log.V(1).Info("renewSelfLease failed; will retry", "err", err)
		}
		if err := r.refreshPeers(ctx); err != nil && ctx.Err() == nil {
			r.log.V(1).Info("refreshPeers failed; will retry", "err", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			// loop
		}
	}
}

// renewSelfLease upserts our own Lease with fresh RenewTime and duration.
func (r *leaseRegistry) renewSelfLease(ctx context.Context) error {
	now := metav1.MicroTime{Time: time.Now()}
	ttlSec := int32(r.ttl / time.Second)
	name := fmt.Sprintf("%s-%s", r.namePrefix, r.self.ID)

	lease := &coordv1.Lease{}
	err := r.cli.Get(ctx, crclient.ObjectKey{Namespace: r.ns, Name: name}, lease)
	switch {
	case apierrors.IsNotFound(err):
		lease = &coordv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: r.ns,
				Name:      name,
				Labels: map[string]string{
					labelPartOf: partOfValue,
					labelPeer:   "true",
					labelPrefix: r.namePrefix,
				},
				Annotations: map[string]string{
					annotWeight: strconv.FormatUint(uint64(r.self.Weight), 10),
				},
			},
			Spec: coordv1.LeaseSpec{
				HolderIdentity:       ptr.To(r.self.ID),
				RenewTime:            &now,
				LeaseDurationSeconds: ptr.To(ttlSec),
			},
		}
		return r.cli.Create(ctx, lease)

	case err != nil:
		return err

	default:
		// Update the existing Lease
		lease.Spec.HolderIdentity = ptr.To(r.self.ID)
		lease.Spec.RenewTime = &now
		lease.Spec.LeaseDurationSeconds = ptr.To(ttlSec)
		if lease.Annotations == nil {
			lease.Annotations = map[string]string{}
		}
		lease.Annotations[annotWeight] = strconv.FormatUint(uint64(r.self.Weight), 10)
		return r.cli.Update(ctx, lease)
	}
}

// refreshPeers lists peer Leases and updates the in-memory snapshot.
func (r *leaseRegistry) refreshPeers(ctx context.Context) error {
	list := &coordv1.LeaseList{}
	// Only list our labeled peer leases with our prefix for efficiency.
	if err := r.cli.List(ctx, list,
		crclient.InNamespace(r.ns),
		crclient.MatchingLabels{
			labelPeer:   "true",
			labelPrefix: r.namePrefix,
		},
	); err != nil {
		return err
	}

	now := time.Now()
	next := make(map[string]sharder.PeerInfo, len(list.Items))

	for i := range list.Items {
		l := &list.Items[i]
		// Basic sanity for holder identity
		if l.Spec.HolderIdentity == nil || *l.Spec.HolderIdentity == "" {
			continue
		}
		id := *l.Spec.HolderIdentity

		// Respect expiry: RenewTime + LeaseDurationSeconds
		if l.Spec.RenewTime == nil || l.Spec.LeaseDurationSeconds == nil {
			// If missing, treat as expired/stale.
			continue
		}
		exp := l.Spec.RenewTime.Time.Add(time.Duration(*l.Spec.LeaseDurationSeconds) * time.Second)
		if now.After(exp) {
			continue // stale peer
		}

		// Weight from annotation (optional)
		weight := defaultWeight
		if wStr := l.Annotations[annotWeight]; wStr != "" {
			if w64, err := strconv.ParseUint(wStr, 10, 32); err == nil && w64 > 0 {
				weight = uint32(w64)
			}
		}

		next[id] = sharder.PeerInfo{ID: id, Weight: weight}
	}

	// Store snapshot (including ourselves; if not listed yet, ensure we're present).
	next[r.self.ID] = sharder.PeerInfo{ID: r.self.ID, Weight: r.self.Weight}

	r.mu.Lock()
	r.peers = next
	r.mu.Unlock()
	return nil
}
