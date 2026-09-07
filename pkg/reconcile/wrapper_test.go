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

package reconcile

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

type stubClusterGetter struct {
	owned map[multicluster.ClusterName]bool
}

func (g *stubClusterGetter) GetCluster(_ context.Context, name multicluster.ClusterName) (cluster.Cluster, error) {
	if g.owned[name] {
		return nil, nil
	}
	return nil, multicluster.ErrClusterNotOwned
}

type countingReconciler struct {
	calls int
}

func (r *countingReconciler) Reconcile(context.Context, Request) (reconcile.Result, error) {
	r.calls++
	return reconcile.Result{}, nil
}

func TestOwnershipWrapper_SkipsNonOwnedCluster(t *testing.T) {
	inner := &countingReconciler{}
	mgr := &stubClusterGetter{owned: map[multicluster.ClusterName]bool{"owned-cluster": true}}
	wrapped := NewOwnershipWrapper(inner, mgr)

	req := Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: "obj"}},
		ClusterName: "not-owned-cluster",
	}

	res, err := wrapped.Reconcile(t.Context(), req)
	if err != nil {
		t.Fatalf("expected no error for a not-owned cluster, got: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected an empty, non-requeueing result, got: %+v", res)
	}
	if inner.calls != 0 {
		t.Fatalf("expected the wrapped reconciler not to be called, called %d times", inner.calls)
	}
}

func TestOwnershipWrapper_CallsThroughForOwnedCluster(t *testing.T) {
	inner := &countingReconciler{}
	mgr := &stubClusterGetter{owned: map[multicluster.ClusterName]bool{"owned-cluster": true}}
	wrapped := NewOwnershipWrapper(inner, mgr)

	req := Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: "obj"}},
		ClusterName: "owned-cluster",
	}

	if _, err := wrapped.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected the wrapped reconciler to be called once, called %d times", inner.calls)
	}
}

func TestOwnershipWrapper_CallsThroughForLocalCluster(t *testing.T) {
	inner := &countingReconciler{}
	// No clusters known to the getter at all; the local cluster must still
	// pass through without ever consulting it.
	mgr := &stubClusterGetter{}
	wrapped := NewOwnershipWrapper(inner, mgr)

	req := Request{Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "obj"}}}

	if _, err := wrapped.Reconcile(t.Context(), req); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("expected the wrapped reconciler to be called once for the local cluster, called %d times", inner.calls)
	}
}

func TestOwnershipWrapper_PropagatesOtherErrors(t *testing.T) {
	boom := errors.New("boom")
	inner := Func(func(context.Context, Request) (reconcile.Result, error) {
		return reconcile.Result{}, boom
	})
	mgr := &stubClusterGetter{owned: map[multicluster.ClusterName]bool{"owned-cluster": true}}
	wrapped := NewOwnershipWrapper(inner, mgr)

	req := Request{
		Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: "obj"}},
		ClusterName: "owned-cluster",
	}

	if _, err := wrapped.Reconcile(t.Context(), req); !errors.Is(err, boom) {
		t.Fatalf("expected the wrapped reconciler's error to propagate, got: %v", err)
	}
}
