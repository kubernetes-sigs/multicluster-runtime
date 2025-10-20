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
	"time"

	"sigs.k8s.io/multicluster-runtime/pkg/manager/peers"
	"sigs.k8s.io/multicluster-runtime/pkg/manager/sharder"
)

// Option mutates mcManager configuration.
type Option func(*mcManager)

// WithSharder replaces the default HRW sharder.
func WithSharder(s sharder.Sharder) Option {
	return func(m *mcManager) {
		if m.engine != nil {
			m.engine.sharder = s
		}
	}
}

// WithPeerWeight allows heterogeneous peers (capacity hint).
// Effective share tends toward w_i/Σw under many shards.
func WithPeerWeight(w uint32) Option {
	return func(m *mcManager) {
		if m.engine != nil {
			m.engine.cfg.PeerWeight = w
		}
	}
}

// WithShardLease configures the fencing Lease ns/prefix (mcr-shard-* by default).
func WithShardLease(ns, name string) Option {
	return func(m *mcManager) {
		if m.engine != nil {
			m.engine.cfg.FenceNS = ns
			m.engine.cfg.FencePrefix = name
		}
	}
}

// WithPerClusterLease enables/disables per-cluster fencing (true -> mcr-shard-<cluster>).
func WithPerClusterLease(on bool) Option {
	return func(m *mcManager) {
		if m.engine != nil {
			m.engine.cfg.PerClusterLease = on
		}
	}
}

// WithSynchronizationIntervals tunes the synchronization probe/rehash cadences.
func WithSynchronizationIntervals(probe, rehash time.Duration) Option {
	return func(m *mcManager) {
		if m.engine != nil {
			m.engine.cfg.Probe = probe
			m.engine.cfg.Rehash = rehash
		}
	}
}

// WithLeaseTimings configures fencing lease timings.
// Choose renew < duration (e.g., renew ≈ duration/3).
func WithLeaseTimings(duration, renew, throttle time.Duration) Option {
	return func(m *mcManager) {
		if m.engine != nil {
			m.engine.cfg.LeaseDuration = duration
			m.engine.cfg.LeaseRenew = renew
			m.engine.cfg.FenceThrottle = throttle
		}
	}
}

// WithPeerRegistry injects a custom peer Registry. When set, it overrides the
// default Lease-based registry. Peer weight should be provided by the custom
// registry; WithPeerWeight does not apply.
func WithPeerRegistry(reg peers.Registry) Option {
	return func(m *mcManager) {
		if m.engine != nil && reg != nil {
			m.engine.peers = reg
			m.engine.self = reg.Self()
		}
	}
}
