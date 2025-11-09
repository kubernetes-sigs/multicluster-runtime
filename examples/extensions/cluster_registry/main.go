/*
Copyright 2024 The Kubernetes Authors.

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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/multicluster-runtime/pkg/extensions/cluster_registry"

	ctrl "sigs.k8s.io/controller-runtime"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/providers/kind"
)

func main() {
	ctrllog.SetLogger(zap.New(zap.UseDevMode(true)))
	entryLog := ctrllog.Log.WithName("entrypoint")
	ctx := signals.SetupSignalHandler()

	provider := kind.New()
	mgr, err := mcmanager.New(ctrl.GetConfigOrDie(), provider, mcmanager.Options{})
	if err != nil {
		entryLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	clusterRegistry := cluster_registry.New()
	err = mgr.Add(clusterRegistry)
	if err != nil {
		entryLog.Error(err, "unable to add cluster_registry")
		os.Exit(1)
	}

	err = mcbuilder.ControllerManagedBy(mgr).
		Named("multicluster-cluster-registry").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(
			func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
				log := ctrllog.FromContext(ctx).WithValues("cluster", req.ClusterName)
				log.Info("Reconciling ConfigMap by coping to other clusters")
				ccSource, err := mgr.GetCluster(ctx, req.ClusterName)
				if err != nil {
					return reconcile.Result{}, err
				}

				confMapSource := &corev1.ConfigMap{}
				if err := ccSource.GetClient().Get(ctx, req.Request.NamespacedName, confMapSource); err != nil {
					if apierrors.IsNotFound(err) {
						// delete from other clusters if you want
						return reconcile.Result{}, nil
					}
					return reconcile.Result{}, err
				}

				for _, clName := range clusterRegistry.ClusterNames() {
					// custom logic below
					if clName == req.ClusterName {
						continue // skip source cluster
					}
					cc, err := mgr.GetCluster(ctx, clName)
					if err != nil {
						return reconcile.Result{}, fmt.Errorf("cannot find client for cluster %s :: %w", clName, err)
					}
					// copy to other clusters only if it does not exist. Updates are not synchronized in this example.
					confMap := &corev1.ConfigMap{}
					err = cc.GetClient().Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, confMap)
					if apierrors.IsNotFound(err) {
						confMapCopy := confMapSource.DeepCopy()
						confMapCopy.SetResourceVersion("")
						err = cc.GetClient().Create(ctx, confMapCopy)
						if err != nil {
							return reconcile.Result{}, fmt.Errorf("cannot create configMap on cluster %s :: %w", clName, err)
						}
						ccSource.GetEventRecorderFor("multicluster-cluster-registry").Event(
							confMapSource,
							corev1.EventTypeNormal,
							"ConfigMapFound",
							fmt.Sprintf("ConfigMap found in cluster %s is going to be synchornized.", req.ClusterName),
						)
					}
				}
				return ctrl.Result{}, nil
			},
		))
	if err != nil {
		entryLog.Error(err, "unable to create controller")
		os.Exit(1)
	}

	if err := mgr.Start(ctx); ignoreCanceled(err) != nil {
		entryLog.Error(err, "unable to start")
		return
	}
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
