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

package acceptance

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// Provider runs acceptance tests for providers.
// The manager must not be started, it will be started and stopped as
// part of the acceptance tests.
// If the provider needs to be started it must implement the
// multicluster.ProviderRunnable interface to be started by the manager.
func Provider(t testing.TB, clusterGenerator ClusterGenerator, manager mcmanager.Manager) {
	t.Log("Starting acceptance tests")

	// This is a sanity check to verify that the manager is not started.
	// Health checks can only be added if the manager is not running,
	// otherwise the add methods return an error.
	if err := manager.AddHealthzCheck("acceptance", healthz.Ping); err != nil {
		t.Fatalf("Failed to add healthz check, ensure the manager is not started: %v", err)
	}

	t.Log("Creating a cluster before starting the manager")
	clusterBeforeName, clusterBeforeCfg := createCluster(t, clusterGenerator)
	clusterBeforeTest := "created before manager start"
	writeConfigMap(t, clusterBeforeCfg, "before", clusterBeforeTest)

	t.Log("Starting the manager")
	managerCtx, managerCancel := context.WithCancel(t.Context())
	managerErr := make(chan error, 1)
	defer managerCancel()
	go func() {
		// Ginkgo maps their equivalent of testing.TB.Errorf to
		// Ginkgo.Errorf which is equivalent to Fatalf as it immediately
		// stops execution of the test by panicking instead of marking
		// the test as failed and continuing.
		//
		// This is caught by ginkgo later, _but_ causes it to
		// discard _all_ information about the run and just print
		// a boilerplate text about using GinkgoRecover.
		//
		// Instead the error is written to a channel to be read later in
		// the main goroutine.
		managerErr <- ignoreCanceled(manager.Start(managerCtx))
	}()

	t.Log("Wait for manager to win the election")
	func() {
		timeoutCtx, timeoutCancel := context.WithTimeout(t.Context(), WaitTimeout)
		defer timeoutCancel()
		select {
		case <-manager.Elected():
			t.Log("Manager elected")
		case <-timeoutCtx.Done():
			if !errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
				t.Fatalf("Manager not elected within timeout")
			}
		}
	}()

	t.Logf("Retrieve cluster %q, created before manager", clusterBeforeName)
	clusterBefore := getCluster(t, manager, clusterBeforeName, clusterBeforeCfg)
	clusterBeforeData := getConfigMap(t, clusterBefore.GetConfig(), "before")
	if clusterBeforeData != clusterBeforeTest {
		t.Errorf("Cluster data mismatch: got %q, want %q", clusterBeforeData, "created before manager start")
	}

	t.Logf("Creating a cluster after starting the manager")
	clusterAfterName, clusterAfterCfg := createCluster(t, clusterGenerator)
	clusterAfterTest := "created after manager start"
	writeConfigMap(t, clusterAfterCfg, "after", clusterAfterTest)

	t.Logf("Retrieve cluster %q, created after the manager", clusterAfterName)
	clusterAfter := getCluster(t, manager, clusterAfterName, clusterAfterCfg)
	clusterAfterData := getConfigMap(t, clusterAfter.GetConfig(), "after")
	if clusterAfterData != clusterAfterTest {
		t.Errorf("Cluster data mismatch: got %q, want %q", clusterAfterData, "created after manager start")
	}

	// TODO could be subtest/function
	t.Logf("Verify return of %q for unknown cluster", multicluster.ErrClusterNotFound)
	_, err := manager.GetCluster(t.Context(), UnknownClusterName)
	if !errors.Is(err, multicluster.ErrClusterNotFound) {
		t.Errorf("GetCluster(%q) = %v, want ErrClusterNotFound", UnknownClusterName, err)
	}

	// TODO entire index could be its own function
	t.Logf("Index configmap data.data field")
	if err := manager.GetFieldIndexer().IndexField(t.Context(), &corev1.ConfigMap{}, "data",
		func(obj client.Object) []string {
			cm, ok := obj.(*corev1.ConfigMap)
			if !ok {
				return nil
			}
			if val, ok := cm.Data["data"]; ok {
				return []string{val}
			}
			return []string{}
		},
	); err != nil {
		t.Errorf("Failed to index configmap data.data field: %v", err)
	}
	t.Logf("Field indexed, retrieving configmap by field")
	cms := &corev1.ConfigMapList{}
	timeoutCtx, timeoutCancel := context.WithTimeout(t.Context(), 10*time.Second) // TODO temporary, odd failure
	defer timeoutCancel()
	if err := clusterBefore.GetCache().List(timeoutCtx, cms, client.MatchingFields{"data": clusterBeforeTest}); err != nil {
		t.Fatalf("Failed to list configmaps in cluster %q: %v", clusterBeforeName, err)
	}
	if len(cms.Items) != 1 {
		t.Errorf("Expected 1 configmap in cluster %q, got %d", clusterBeforeName, len(cms.Items))
	}

	t.Log("Create new cluster after indexing field")
	clusterIndexName, clusterIndexCfg := createCluster(t, clusterGenerator)
	clusterIndexTest := "created after indexing"
	writeConfigMap(t, clusterIndexCfg, "index", clusterIndexTest)

	t.Logf("Retrieve cluster %q, created after indexing field", clusterIndexName)
	clusterIndex := getCluster(t, manager, clusterIndexName, clusterIndexCfg)
	cms = &corev1.ConfigMapList{}
	if err := clusterIndex.GetCache().List(t.Context(), cms, client.MatchingFields{"data": clusterIndexTest}); err != nil {
		t.Fatalf("Failed to list configmaps in cluster %q: %v", clusterIndexName, err)
	}
	if len(cms.Items) != 1 {
		t.Errorf("Expected 1 configmap in cluster %q, got %d", clusterIndexName, len(cms.Items))
	}
	// end index

	// TODO cluster removal can be its own function
	t.Log("Test that a cluster is removed when the backing cluster is gone")
	clusterCtx, clusterCancel := context.WithCancel(t.Context())
	clusterToRemoveName, clusterToRemoveCfg, err := clusterGenerator(clusterCtx, errorHandler(t, "removable cluster"))
	if err != nil {
		t.Fatalf("Failed to create cluster: %v", err)
	}

	t.Logf("Validate that cluster to remove is available, %q", clusterToRemoveName)
	getCluster(t, manager, clusterToRemoveName, clusterToRemoveCfg)

	t.Logf("Cancelling context for cluster %q", clusterToRemoveName)
	clusterCancel()
	eventually(t, func() error {
		_, err := manager.GetCluster(t.Context(), clusterToRemoveName)
		if err == nil {
			return errors.New("cluster still exists")
		}
		return nil
	}, WaitTimeout, PollInterval, "cluster %q not removed", clusterToRemoveName)
	// end cluster removal

	t.Log("Cancelling the manager context")
	managerCancel()

	if err := <-managerErr; err != nil {
		t.Errorf("Manager returned error: %v", err)
	}
}
