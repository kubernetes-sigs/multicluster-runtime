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

package controllers

import (
	"context"
	"time"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// PodWatcher is a simple controller that watches pods across multiple clusters
type PodWatcher struct {
	Manager mcmanager.Manager
	Log     logr.Logger
}

// NewPodWatcher creates a new PodWatcher
func NewPodWatcher(mgr mcmanager.Manager) *PodWatcher {
	return &PodWatcher{
		Manager: mgr,
		Log:     ctrllog.Log.WithName("pod-watcher"),
	}
}

// Start implements Runnable
func (p *PodWatcher) Start(ctx context.Context) error {
	// Nothing to do here - we'll handle everything in Engage
	return nil
}

// Engage implements multicluster.Aware and gets called when a new cluster is engaged
func (p *PodWatcher) Engage(ctx context.Context, clusterName string, cl cluster.Cluster) error {
	log := p.Log.WithValues("cluster", clusterName)
	log.Info("Engaging cluster")

	// Start a goroutine to periodically list pods
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		// Initial list
		if err := p.listPods(ctx, cl, clusterName, log); err != nil {
			log.Error(err, "Failed to list pods")
		}

		for {
			select {
			case <-ctx.Done():
				log.Info("Context done, stopping pod watcher")
				return
			case <-ticker.C:
				if err := p.listPods(ctx, cl, clusterName, log); err != nil {
					log.Error(err, "Failed to list pods")
				}
			}
		}
	}()

	return nil
}

// listPods lists pods in the default namespace
func (p *PodWatcher) listPods(ctx context.Context, cl cluster.Cluster, clusterName string, log logr.Logger) error {
	var pods corev1.PodList
	if err := cl.GetClient().List(ctx, &pods, &client.ListOptions{
		Namespace: "default",
	}); err != nil {
		return err
	}

	log.Info("Pods in default namespace", "count", len(pods.Items))
	for _, pod := range pods.Items {
		log.Info("Pod",
			"name", pod.Name,
			"status", pod.Status.Phase)
	}

	return nil
}
