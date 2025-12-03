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
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/multicluster-runtime/pkg/manager/peers"
	"sigs.k8s.io/multicluster-runtime/pkg/manager/sharder"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	"sigs.k8s.io/multicluster-runtime/pkg/util"
)

// SynchronizationConfig holds the knobs for shard synchronization and fencing.
//
// Fencing:
//   - FenceNS/FencePrefix: namespace and name prefix for per-shard Lease objects
//     (e.g. mcr-shard-<cluster> when PerClusterLease is true).
//   - PerClusterLease: when true, use one Lease per cluster (recommended). When false,
//     use a single shared fence name for all clusters.
//   - LeaseDuration / LeaseRenew: total TTL and renew cadence. Choose renew < duration
//     (commonly renew ≈ duration/3).
//   - FenceThrottle: backoff before retrying failed fence acquisition, to reduce API churn.
//
// Peers (membership):
//   - PeerPrefix: prefix used by the peer registry for membership Leases.
//   - PeerWeight: relative capacity hint used by the sharder (0 treated as 1).
//
// Cadence:
//   - Probe: periodic synchronization tick interval (decision loop).
//   - Rehash: optional slower cadence for planned redistribution (unused in basic HRW).
type SynchronizationConfig struct {
	// FenceNS is the namespace where fence Leases live (usually "kube-system").
	FenceNS string
	// FencePrefix is the base Lease name for fences; with PerClusterLease it becomes
	// FencePrefix+"-"+<cluster> (e.g., "mcr-shard-zoo").
	FencePrefix string
	// PerClusterLease controls whether we create one fence per cluster (true) or a
	// single shared fence name (false). Per-cluster is recommended.
	PerClusterLease bool
	// LeaseDuration is the total TTL written to the Lease (spec.LeaseDurationSeconds).
	LeaseDuration time.Duration
	// LeaseRenew is the renewal cadence; must be < LeaseDuration (rule of thumb ≈ 1/3).
	LeaseRenew time.Duration
	// FenceThrottle backs off repeated failed TryAcquire attempts to reduce API churn.
	FenceThrottle time.Duration

	// PeerPrefix is the Lease name prefix used by the peer registry for membership.
	PeerPrefix string
	// PeerWeight is this peer’s capacity hint for HRW (0 treated as 1).
	PeerWeight uint32

	// Probe is the synchronization decision loop interval.
	Probe time.Duration
	// Rehash is an optional slower cadence for planned redistribution (currently unused).
	Rehash time.Duration
}

// synchronizationEngine makes synchronization decisions and starts/stops per-cluster work.
//
// It combines:
//   - a peerRegistry (live peer snapshot),
//   - a sharder (e.g., HRW) to decide "who should own",
//   - a per-cluster leaseGuard to fence "who actually runs".
//
// The engine keeps per-cluster engagement state, ties watches/workers to an
// engagement context, and uses the fence to guarantee single-writer semantics.
type synchronizationEngine struct {
	// kube is the host cluster client used for Leases and provider operations.
	kube client.Client
	// log is the engine’s logger.
	log logr.Logger
	// sharder decides “shouldOwn” given clusterID, peers, and self (e.g., HRW).
	sharder sharder.Sharder
	// peers provides a live membership snapshot for sharding decisions.
	peers peers.Registry
	// self is this process’s identity/weight as known by the peer registry.
	self sharder.PeerInfo
	// cfg holds all synchronization/fencing configuration.
	cfg SynchronizationConfig

	// mu guards engaged and runnables.
	mu sync.Mutex
	// engaged tracks per-cluster engagement state keyed by cluster name.
	engaged map[string]*engagement
	// runnables are the multicluster components to (de)start per cluster.
	runnables []multicluster.Aware
}

// engagement tracks per-cluster lifecycle within the engine.
//
// ctx/cancel: engagement context; cancellation stops sources/workers.
// started: whether runnables have been engaged for this cluster.
// fence: the per-cluster Lease guard (nil until first start attempt).
// nextTry: throttle timestamp for fence acquisition retries.
type engagement struct {
	// name is the cluster identifier (e.g., namespace).
	name string
	// cl is the controller-runtime cluster handle for this engagement.
	cl cluster.Cluster
	// ctx is the engagement context; cancelling it stops sources/work.
	ctx context.Context
	// cancel cancels ctx.
	cancel context.CancelFunc
	// started is true after runnables have been engaged for this cluster.
	started bool

	// fence is the per-cluster Lease guard (nil until first start attempt).
	fence *leaseGuard
	// nextTry defers the next TryAcquire attempt until this time (throttling).
	nextTry time.Time
}

// newSynchronizationEngine wires an engine with its dependencies and initial config.
func newSynchronizationEngine(kube client.Client, log logr.Logger, shard sharder.Sharder, peers peers.Registry, self sharder.PeerInfo, cfg SynchronizationConfig) *synchronizationEngine {
	return &synchronizationEngine{
		kube: kube, log: log,
		sharder: shard, peers: peers, self: self, cfg: cfg,
		engaged: make(map[string]*engagement),
	}
}

func (e *synchronizationEngine) fenceName(cluster string) string {
	// Per-cluster fence: mcr-shard-<cluster>; otherwise a single global fence
	if e.cfg.PerClusterLease {
		return fmt.Sprintf("%s-%s", e.cfg.FencePrefix, util.SanitizeDNS1123(cluster))
	}
	return e.cfg.FencePrefix
}

// Runnable returns a Runnable that manages synchronization of clusters.
func (e *synchronizationEngine) Runnable() manager.Runnable {
	return manager.RunnableFunc(func(ctx context.Context) error {
		e.log.Info("synchronization runnable starting", "peer", e.self.ID)
		errCh := make(chan error, 1)
		go func() { errCh <- e.peers.Run(ctx) }()
		e.recompute(ctx)
		t := time.NewTicker(e.cfg.Probe)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case err := <-errCh:
				return err
			case <-t.C:
				e.log.V(1).Info("synchronization tick", "peers", len(e.peers.Snapshot()))
				e.recompute(ctx)
			}
		}
	})
}

// Engage registers a cluster for synchronization management.
func (e *synchronizationEngine) Engage(parent context.Context, name string, cl cluster.Cluster) error {
	// If provider already canceled, don't engage a dead cluster.
	if err := parent.Err(); err != nil {
		return err
	}

	var doRecompute bool

	e.mu.Lock()
	defer e.mu.Unlock()

	if cur, ok := e.engaged[name]; ok {
		// Re-engage same name: replace token; stop old engagement; preserve fence.
		var fence *leaseGuard
		if cur.fence != nil {
			fence = cur.fence // keep the existing fence/renewer
		}
		if cur.cancel != nil {
			cur.cancel() // stop old sources/workers
		}

		newEng := &engagement{name: name, cl: cl, fence: fence}
		e.engaged[name] = newEng

		// cleanup tied to the *new* token; old goroutine will no-op (ee != token)
		go func(pctx context.Context, key string, token *engagement) {
			<-pctx.Done()
			e.mu.Lock()
			if ee, ok := e.engaged[key]; ok && ee == token {
				if ee.started && ee.cancel != nil {
					ee.cancel()
				}
				if ee.fence != nil {
					go ee.fence.Release(context.Background())
				}
				delete(e.engaged, key)
			}
			e.mu.Unlock()
		}(parent, name, newEng)

		doRecompute = true
	} else {
		eng := &engagement{name: name, cl: cl}
		e.engaged[name] = eng
		go func(pctx context.Context, key string, token *engagement) {
			<-pctx.Done()
			e.mu.Lock()
			if ee, ok := e.engaged[key]; ok && ee == token {
				if ee.started && ee.cancel != nil {
					ee.cancel()
				}
				if ee.fence != nil {
					go ee.fence.Release(context.Background())
				}
				delete(e.engaged, key)
			}
			e.mu.Unlock()
		}(parent, name, eng)
		doRecompute = true
	}

	// Kick a decision outside the lock for faster attach.
	if doRecompute {
		go e.recompute(parent)
	}

	return nil
}

// recompute checks the current synchronization state and starts/stops clusters as needed.
func (e *synchronizationEngine) recompute(parent context.Context) {
	peers := e.peers.Snapshot()
	self := e.self

	type toStart struct {
		name string
		cl   cluster.Cluster
		ctx  context.Context
	}
	var starts []toStart
	var stops []context.CancelFunc

	now := time.Now()

	e.mu.Lock()
	defer e.mu.Unlock()
	for name, engm := range e.engaged {
		should := e.sharder.ShouldOwn(name, peers, self)

		switch {
		case should && !engm.started:
			// ensure fence exists
			if engm.fence == nil {
				onLost := func(cluster string) func() {
					return func() {
						// best-effort stop if we lose the lease mid-flight
						e.log.Info("lease lost; stopping", "cluster", cluster, "peer", self.ID)
						e.mu.Lock()
						if ee := e.engaged[cluster]; ee != nil && ee.started && ee.cancel != nil {
							ee.cancel()
							ee.started = false
						}
						e.mu.Unlock()
					}
				}(name)
				engm.fence = newLeaseGuard(
					e.kube,
					e.cfg.FenceNS, e.fenceName(name), e.self.ID,
					e.cfg.LeaseDuration, e.cfg.LeaseRenew, onLost,
				)
			}

			// throttle attempts
			if now.Before(engm.nextTry) {
				continue
			}

			// try to take the fence; if we fail, set nextTry and retry on next tick
			if !engm.fence.TryAcquire(parent) {
				engm.nextTry = now.Add(e.cfg.FenceThrottle)
				continue
			}

			// acquired fence; start the cluster
			ctx, cancel := context.WithCancel(parent)
			engm.ctx, engm.cancel, engm.started = ctx, cancel, true
			starts = append(starts, toStart{name: engm.name, cl: engm.cl, ctx: ctx})
			e.log.Info("synchronization start", "cluster", name, "peer", self.ID)

		case !should && engm.started:
			// stop + release fence
			if engm.cancel != nil {
				stops = append(stops, engm.cancel)
			}
			if engm.fence != nil {
				go engm.fence.Release(parent)
			}
			engm.cancel = nil
			engm.started = false
			engm.nextTry = time.Time{}
			e.log.Info("synchronization stop", "cluster", name, "peer", self.ID)
		}
	}
	for _, c := range stops {
		c()
	}
	for _, s := range starts {
		go e.startForCluster(s.ctx, s.name, s.cl)
	}
}

// startForCluster engages all runnables for the given cluster.
func (e *synchronizationEngine) startForCluster(ctx context.Context, name string, cl cluster.Cluster) {
	for _, r := range e.runnables {
		if err := r.Engage(ctx, name, cl); err != nil {
			e.log.Error(err, "failed to engage", "cluster", name)
			// best-effort: cancel + mark stopped so next tick can retry
			e.mu.Lock()
			if engm := e.engaged[name]; engm != nil && engm.cancel != nil {
				engm.cancel()
				engm.started = false
			}
			e.mu.Unlock()
			return
		}
	}
}

func (e *synchronizationEngine) AddRunnable(r multicluster.Aware) {
	e.runnables = append(e.runnables, r)
}
