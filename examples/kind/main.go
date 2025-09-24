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
	"os"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

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

	err = mcbuilder.ControllerManagedBy(mgr).
		Named("multicluster-configmaps").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(
			func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
				log := ctrllog.FromContext(ctx).WithValues("cluster", req.ClusterName)
				log.Info("Reconciling ConfigMap")

				cl, err := mgr.GetCluster(ctx, req.ClusterName)
				if err != nil {
					return reconcile.Result{}, err
				}

				cm := &corev1.ConfigMap{}
				if err := cl.GetClient().Get(ctx, req.Request.NamespacedName, cm); err != nil {
					if apierrors.IsNotFound(err) {
						return reconcile.Result{}, nil
					}
					return reconcile.Result{}, err
				}

				cl.GetEventRecorderFor("kind-multicluster-configmaps").Event(
					cm,
					corev1.EventTypeNormal,
					"ConfigMapFound",
					"ConfigMap found in cluster "+req.ClusterName,
				)

				log.Info("ConfigMap found", "namespace", cm.Namespace, "name", cm.Name, "cluster", req.ClusterName)

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
