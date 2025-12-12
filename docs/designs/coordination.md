# Coordination and Sharding (Experimental)

This document explains how multicluster-runtime coordinates per-cluster lifecycle, with a focus on the sharding coordinator that uses rendezvous hashing and Kubernetes Leases for fencing. The surface described here is **experimental** and may change without notice.

## Goals

- Single-writer semantics per remote cluster while allowing many controller instances to run.
- Deterministic, balanced ownership decisions that react quickly to peer loss.
- Clear separation between cluster discovery (providers) and ownership/fencing (coordinator).

## Non-goals

- Intra-cluster sharding (per-object) is out of scope.
- Cross-process distributed transactions; fencing is best-effort via Leases.

## Coordinator responsibilities

The coordinator sits between the multicluster provider and the multicluster-aware runnables:

- Track available clusters (via `Engage` from the provider).
- Decide whether the local process should own a cluster.
- Fence ownership so only one process is active per cluster at a time.
- Start/stop the registered runnables for each cluster according to ownership.

In the current codebase, this is exposed as a pluggable `Coordinator` interface. The default coordinator engages every cluster (legacy behavior); the sharded coordinator lives in `pkg/manager/coordinator/sharded` and is wired via `WithCoordinator(sharded.New(...))`.

## Sharding coordinator design

The sharding coordinator combines three building blocks:

1. **Peer registry (`pkg/manager/coordinator/sharded/peers`)**  
   - Each process writes/renews a peer Lease (`<peerPrefix>-<id>`, default `mcr-peer-<hostname>`) in `FenceNS` (default `kube-system`).  
   - Lists peer Leases with the matching prefix to maintain a live snapshot (ID, weight).  
   - Weight hints relative capacity (default 1).

2. **Sharder (`pkg/manager/coordinator/sharded/sharder`)**  
   - Default is HRW (rendezvous) hashing: given `(clusterID, peers[], self)` pick the peer with the highest score; weighted by peer weight.  
   - Deterministic across peers for the same inputs.

3. **Fencing (`pkg/manager/coordinator/sharded/leaseguard.go`)**  
   - Per-cluster (default) or global Lease (`<fencePrefix>-<cluster>` with sanitization; default `mcr-shard-<cluster>`).  
   - `HolderIdentity` is the peer ID; LeaseDuration (default 20s) renewed on each tick; Renew cadence (default 10s).  
   - Throttles failed acquisition attempts to reduce API churn.

### Lifecycle

1. **Engage cluster**: When a provider discovers a cluster, it calls `Engage(ctx, name, cluster)`. The coordinator records the engagement (preserving any existing fence if re-engaging).
2. **Decision loop**: On start and every `Probe` interval (default 5s):
   - Refresh peer snapshot (peer registry runs in the background).
   - Run HRW to decide if `self` should own each engaged cluster.
   - If not the owner: cancel the engagement context and stop renewing the fence (Lease will expire).
   - If owner: try to acquire/renew the fence Lease. If acquisition fails, wait for the next tick/backoff.
   - After a successful fence acquisition, engage all registered multicluster runnables for that cluster.
3. **Disengage/failover**:
   - If the process stops renewing (crash or ctx cancel), the fence expires after `LeaseDuration` and another peer can take over on the next tick.
   - Ownership changes when the peer set or weights change; HRW reassigns and fencing enforces single-writer.

### Configuration (defaults from sharded.New)

- `FenceNS`: namespace for fences and peer Leases (`kube-system`).
- `FencePrefix`: base fence Lease name (`mcr-shard`).
- `PerClusterLease`: true -> one fence per cluster (recommended); false -> one global fence.
- `LeaseDuration`: 20s; `LeaseRenew`: 10s; `FenceThrottle`: 750ms.
- `PeerPrefix`: peer Lease prefix (`mcr-peer`); `PeerWeight`: 1.
- `Probe`: 5s; `Rehash`: 15s (reserved, not used by HRW today).

`WithMultiCluster` defaults to the basic coordinator (engage all clusters). To enable sharding, supply `WithCoordinator(sharded.New(...))` with the desired options.

### Scalability notes

- Overhead is proportional to clusters: ~1 peer Lease per process, ~1 fence Lease per active cluster.
- With 1,000 clusters and a 10s renew cadence, expect on the order of 100 write ops/sec across all owners. Increase `LeaseDuration`/`LeaseRenew` to reduce churn (at the cost of slower failover), or disable per-cluster fencing to avoid per-cluster Leases (at the cost of weaker isolation).

### Failure handling

- Lease expirations drive failover; no explicit release is required.
- Throttling prevents tight retry loops when a fence is contested or the API is temporarily unavailable.

## Future work

- Revisit defaults to allow opt-in sharding with explicit config (namespace/prefix) or leave basic coordinator as the default.
