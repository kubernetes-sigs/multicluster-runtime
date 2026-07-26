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

package multi

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/cluster"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// ManagerSetupProvider wraps a provider that must setup a controller with the manager, e.g. to watch in-cluster resources.
type ManagerSetupProvider interface {
	multicluster.Provider
	SetupWithManager(ctx context.Context, mgr mcmanager.Manager) error
}

// wrappedManager wraps the real manager for the ManagerSetupProvider to
// engage with and the aware from the multi provider to engage clusters
// with.
type wrappedManager struct {
	mcmanager.Manager
	aware multicluster.Aware
}

func (m *wrappedManager) Engage(ctx context.Context, name multicluster.ClusterName, cl cluster.Cluster) error {
	return m.aware.Engage(ctx, name, cl)
}

var _ multicluster.ProviderRunnable = &runnableProvider{}

type runnableProvider struct {
	ManagerSetupProvider
	mgr mcmanager.Manager
}

// AsRunnable wraps a ManagerSetupProvider so it can be used as a provider in the multi provider.
// Providers that need to setup a controller for the manager cannot natively be handled as a RunnableProvider.
//
// Note that due to the nature of controllers these providers cannot be removed.
func AsRunnable(p ManagerSetupProvider, mgr mcmanager.Manager) multicluster.Provider {
	return &runnableProvider{ManagerSetupProvider: p, mgr: mgr}
}

// Start implements RunnableProvider.Start and engages the underlying provider with the mcmanager passed to AsRunnable.
func (r *runnableProvider) Start(ctx context.Context, aware multicluster.Aware) error {
	if err := r.SetupWithManager(ctx, &wrappedManager{Manager: r.mgr, aware: aware}); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}
