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

package sharder

// PeerInfo describes a participating peer in sharding decisions.
//
// ID must be a stable, unique identifier for the peer (e.g., pod hostname).
//
// Weight is a relative capacity hint. The current HRW implementation in this
// package treats 0 as 1 and multiplies the peer’s score by Weight. With many
// shards, a peer’s expected share of ownership is roughly proportional to
// Weight relative to the sum of all peers’ weights. If you need stricter,
// provably proportional weighting, use a canonical weighted rendezvous score
// (e.g., s = -ln(u)/w) instead of simple multiplication.
type PeerInfo struct {
	ID     string
	Weight uint32 // optional (default 1)
}

// Sharder chooses whether the local peer should "own" (run) a given cluster.
//
// Implementations must be deterministic across peers given the same inputs.
// ShouldOwn does not enforce ownership; callers should still gate actual work
// behind a per-cluster fence (e.g., a Lease) to guarantee single-writer.
type Sharder interface {
	// ShouldOwn returns true if self is the winner for clusterID among peers.
	// - clusterID: stable identifier for the shard
	// - peers: full membership snapshot (order-independent)
	// - self:  the caller’s PeerInfo
	ShouldOwn(clusterID string, peers []PeerInfo, self PeerInfo) bool
}
