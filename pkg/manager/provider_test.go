/*
Copyright 2026 The Kubernetes Authors.

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

package manager_test

import (
	"context"
	"errors"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// heldByAnother is a leader election lock that another replica holds, so a manager that uses it never leads.
type heldByAnother struct{}

func (heldByAnother) Get(context.Context) (*resourcelock.LeaderElectionRecord, []byte, error) {
	now := metav1.Now()
	return &resourcelock.LeaderElectionRecord{
		HolderIdentity:       "another-replica",
		LeaseDurationSeconds: 3600,
		AcquireTime:          now,
		RenewTime:            now,
	}, []byte("another-replica"), nil
}

func (heldByAnother) Create(context.Context, resourcelock.LeaderElectionRecord) error {
	return errors.New("the lock is held by another replica")
}

func (heldByAnother) Update(context.Context, resourcelock.LeaderElectionRecord) error {
	return errors.New("the lock is held by another replica")
}

func (heldByAnother) RecordEvent(string) {}

func (heldByAnother) Identity() string { return "this-replica" }

func (heldByAnother) Describe() string { return "a lock held by another replica" }

// recordingProvider is a ProviderRunnable that records its start.
type recordingProvider struct {
	started chan struct{}
}

func newRecordingProvider() *recordingProvider {
	return &recordingProvider{started: make(chan struct{})}
}

func (p *recordingProvider) Start(ctx context.Context, _ multicluster.Aware) error {
	close(p.started)
	<-ctx.Done()
	return nil
}

func (p *recordingProvider) Get(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return nil, multicluster.ErrClusterNotFound
}

func (p *recordingProvider) IndexField(context.Context, client.Object, string, client.IndexerFunc) error {
	return nil
}

// electionProvider is a recordingProvider that answers NeedLeaderElection with need.
type electionProvider struct {
	*recordingProvider
	need bool
}

func (p electionProvider) NeedLeaderElection() bool { return p.need }

// alwaysRunning is a runnable that needs no leader election.
type alwaysRunning struct{ manager.RunnableFunc }

func (alwaysRunning) NeedLeaderElection() bool { return false }

// startStandby starts a manager for provider that never leads, as a standby replica's, and returns once the manager
// runs the runnables that need no leader election.
func startStandby(ctx context.Context, provider multicluster.Provider) {
	mgr, err := mcmanager.New(&rest.Config{Host: "https://127.0.0.1:1"}, provider, manager.Options{
		LeaderElection:                      true,
		LeaderElectionID:                    "standby",
		LeaderElectionResourceLockInterface: heldByAnother{},
		Metrics:                             metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())

	running := make(chan struct{})
	Expect(mgr.GetLocalManager().Add(alwaysRunning{func(ctx context.Context) error {
		close(running)
		<-ctx.Done()
		return nil
	}})).To(Succeed())

	ctx, cancel := context.WithCancel(ctx)
	DeferCleanup(cancel)
	go func() {
		_ = mgr.Start(ctx)
	}()
	Eventually(running).WithTimeout(10 * time.Second).Should(BeClosed())
}

var _ = Describe("mcManager Start on a replica that does not lead", func() {
	It("should start a provider that does not need leader election", func(ctx SpecContext) {
		p := newRecordingProvider()
		startStandby(ctx, electionProvider{recordingProvider: p, need: false})
		Eventually(p.started).WithTimeout(5 * time.Second).Should(BeClosed())
	})

	It("should not start a provider that needs leader election", func(ctx SpecContext) {
		p := newRecordingProvider()
		startStandby(ctx, electionProvider{recordingProvider: p, need: true})
		Consistently(p.started).WithTimeout(2 * time.Second).ShouldNot(BeClosed())
	})

	It("should not start a provider that does not implement NeedLeaderElection", func(ctx SpecContext) {
		p := newRecordingProvider()
		startStandby(ctx, p)
		Consistently(p.started).WithTimeout(2 * time.Second).ShouldNot(BeClosed())
	})
})
