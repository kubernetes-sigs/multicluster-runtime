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

package main

import (
	"context"
	"errors"
	"flag"
	"strings"

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
	"sigs.k8s.io/multicluster-runtime/providers/file"
)

var (
	fKubeconfigFiles = flag.String("kubeconfigs", "", "Comma-separated list of kubeconfig file paths to process")
	fKubeconfigDirs  = flag.String("kubeconfig-dirs", "", "Comma-separated list of directories to search for kubeconfig files")
	fGlobs           = flag.String("globs", "", "Comma-separated list of glob patterns to match files")
)

func main() {
	ctrllog.SetLogger(zap.New(zap.UseDevMode(true)))
	entryLog := ctrllog.Log.WithName("entrypoint")
	ctx := signals.SetupSignalHandler()

	flag.Parse()

	kubeconfigFiles := strings.Split(*fKubeconfigFiles, ",")
	if len(kubeconfigFiles) == 1 && kubeconfigFiles[0] == "" {
		kubeconfigFiles = []string{}
	}

	kubeconfigDirs := strings.Split(*fKubeconfigDirs, ",")
	if len(kubeconfigDirs) == 1 && kubeconfigDirs[0] == "" {
		kubeconfigDirs = []string{}
	}

	provider, err := file.New(file.Options{
		KubeconfigFiles: kubeconfigFiles,
		KubeconfigDirs:  kubeconfigDirs,
		KubeconfigGlobs: strings.Split(*fGlobs, ","),
	})
	if err != nil {
		entryLog.Info("Error creating provider: %v\n", err)
		return
	}

	mgr, err := mcmanager.New(ctrl.GetConfigOrDie(), provider, mcmanager.Options{})
	if err != nil {
		entryLog.Info("unable to set up local controller manager: %w", err)
		return
	}

	if err := mcbuilder.ControllerManagedBy(mgr).
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

				log.Info("ConfigMap found", "namespace", cm.Namespace, "name", cm.Name, "cluster", req.ClusterName)

				return ctrl.Result{}, nil
			},
		),
		); err != nil {
		entryLog.Info("failed to build controller: %w", err)
		return
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
