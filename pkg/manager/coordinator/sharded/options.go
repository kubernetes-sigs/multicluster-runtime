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
	"time"

	"sigs.k8s.io/multicluster-runtime/pkg/manager/coordinator/sharded/peers"
	"sigs.k8s.io/multicluster-runtime/pkg/manager/coordinator/sharded/sharder"
)

// WithSharder replaces the default HRW sharder.
func WithSharder(s sharder.Sharder) Option {
	return func(c *Coordinator) {
		if s != nil {
			c.sharder = s
		}
	}
}

// WithPeerWeight allows heterogeneous peers (capacity hint).
// Effective share tends toward w_i/Σw under many shards.
func WithPeerWeight(w uint32) Option {
	return func(c *Coordinator) {
		c.cfg.PeerWeight = w
	}
}

// WithShardLease configures the fencing Lease ns/prefix (mcr-shard-* by default).
func WithShardLease(ns, name string) Option {
	return func(c *Coordinator) {
		c.cfg.FenceNS = ns
		c.cfg.FencePrefix = name
	}
}

// WithPerClusterLease enables/disables per-cluster fencing (true -> mcr-shard-<cluster>).
func WithPerClusterLease(on bool) Option {
	return func(c *Coordinator) {
		c.cfg.PerClusterLease = on
	}
}

// WithSynchronizationIntervals tunes the synchronization probe/rehash cadences.
func WithSynchronizationIntervals(probe, rehash time.Duration) Option {
	return func(c *Coordinator) {
		c.cfg.Probe = probe
		c.cfg.Rehash = rehash
	}
}

// WithLeaseTimings configures fencing lease timings.
func WithLeaseTimings(duration, renew, throttle time.Duration) Option {
	return func(c *Coordinator) {
		c.cfg.LeaseDuration = duration
		c.cfg.LeaseRenew = renew
		c.cfg.FenceThrottle = throttle
	}
}

// WithPeerRegistry injects a custom peer Registry. When set, it overrides the
// default Lease-based registry. Peer weight should be provided by the custom
// registry; WithPeerWeight does not apply.
func WithPeerRegistry(reg peers.Registry) Option {
	return func(c *Coordinator) {
		if reg != nil {
			c.peers = reg
			c.self = reg.Self()
		}
	}
}

// withConfig replaces the full config (for tests only; not exported).
func withConfig(cfg Config) Option {
	return func(c *Coordinator) {
		c.cfg = cfg
	}
}
