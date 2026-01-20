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

import (
	"hash/fnv"
)

// HRW implements Highest-Random-Weight (aka Rendezvous) hashing.
//
// Given a stable cluster identifier and a snapshot of peers, HRW selects
// the peer with the highest score for that cluster. All peers compute the
// same scores independently, so the winner is deterministic as long as
// the inputs (clusterID, peer IDs, weights) are identical.
//
// Weighting: this implementation biases selection by multiplying the
// score with the peer's Weight (0 is treated as 1). This is a simple and
// fast heuristic; if you need proportional weighting with stronger
// distribution guarantees, consider the canonical weighted rendezvous
// formula (e.g., score = -ln(u)/w).
type HRW struct{}

// NewHRW returns a new HRW sharder.
func NewHRW() *HRW { return &HRW{} }

// ShouldOwn returns true if self is the HRW winner for clusterID among peers.
//
// Inputs:
//   - clusterID: a stable string that identifies the shard/cluster (e.g., namespace name).
//   - peers:     the full membership snapshot used for the decision; all participants
//     should use the same list (order does not matter).
//   - self:      the caller's peer info.
//
// Behavior & caveats:
//   - If peers is empty, this returns true (caller may choose to gate with fencing).
//   - Weight 0 is treated as 1 (no special meaning).
//   - Ties are broken by "last wins" in the iteration order (practically
//     unreachable with 64-bit hashing, but documented for completeness).
//   - The hash (FNV-1a, 64-bit) is fast and stable but not cryptographically secure.
//   - This method does not enforce ownership; callers should still use a
//     per-shard fence (e.g., a Lease) to serialize actual work.
//
// Determinism requirements:
//   - All peers must see the same peer IDs and weights when computing.
//   - clusterID must be stable across processes (same input → same winner).
func (h *HRW) ShouldOwn(clusterID string, peers []PeerInfo, self PeerInfo) bool {
	if len(peers) == 0 {
		return true
	}
	var best PeerInfo
	var bestScore uint64
	for _, p := range peers {
		score := hash64(clusterID + "|" + p.ID)
		if p.Weight == 0 {
			p.Weight = 1
		}
		score *= uint64(p.Weight)
		if score >= bestScore {
			bestScore = score
			best = p
		}
	}
	return best.ID == self.ID
}

// hash64 returns a stable 64-bit FNV-1a hash of s.
// Fast, non-cryptographic; suitable for rendezvous hashing.
func hash64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}
