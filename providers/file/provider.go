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

package file

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"gopkg.in/fsnotify.v1"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/multicluster-runtime/pkg/clusters"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}
var _ multicluster.ProviderRunnable = &Provider{}

// Options defines the options for the file-based cluster provider.
type Options struct {
	// KubeconfigFiles are paths to kubeconfig files.
	// The default depends on the KubeconfigDirs variable.
	KubeconfigFiles []string

	// KubeconfigDirs are directories to search for kubeconfig files matching the
	// globs specified in KubeconfigGlobs.
	//
	// If either one or both of KubeconfigFiles or KubeconfigDirs are
	// set both are used as input for the provider.
	// If both are empty defaults are applied in order of precedence:
	// 1. If the KUBECONFIG environment variable is set and contains
	//       a path to a valid file it is used in KubeconfigFiles.
	// 2. If ~/.kube/config exists it is used in KubeconfigFiles.
	// 3. The working directory is used in KubeconfigDirs.
	KubeconfigDirs []string

	// KubeconfigGlobs are the glob patterns to match kubeconfig files
	// in directories.
	// Default is DefaultKubeconfigGlobs.
	KubeconfigGlobs []string

	// Separator is the string used as a separator between the file path
	// and context name when creating cluster names.
	// Default is "+".
	// E.g. a kubeconfig file at /a/b/c/kubeconfig.yaml with a context
	// "my-context" would result in a cluster name
	// "/a/b/c/kubeconfig.yaml+my-context".
	Separator string

	// ClusterOptions is the list of options to pass to the cluster object.
	ClusterOptions []cluster.Option
}

// DefaultKubeconfigGlobs are the default glob patterns when searching
// for kubeconfig files in a directory.
var DefaultKubeconfigGlobs = []string{
	"kubeconfig.yaml",
	"kubeconfig.yml",
	"*.kubeconfig",
	"*.kubeconfig.yaml",
	"*.kubeconfig.yml",
}

func (p *Provider) defaultKubeconfigPaths() ([]string, []string) {
	if envKubeconfig := os.Getenv("KUBECONFIG"); envKubeconfig != "" {
		return []string{envKubeconfig}, []string{}
	}

	defaultKubeconfig := os.ExpandEnv("$HOME/.kube/config")
	if _, err := os.Stat(defaultKubeconfig); err == nil {
		return []string{defaultKubeconfig}, []string{}
	}

	pwd, err := os.Getwd()
	if err != nil {
		p.log.Error(err, "error getting working directory, defaulting to '.'")
		pwd = "."
	}

	return []string{}, []string{pwd}
}

// New returns a new Provider with the given options.
func New(opts Options) (*Provider, error) {
	p := new(Provider)
	p.opts = opts

	if len(p.opts.KubeconfigFiles) == 0 && len(p.opts.KubeconfigDirs) == 0 {
		p.opts.KubeconfigFiles, p.opts.KubeconfigDirs = p.defaultKubeconfigPaths()
	}

	if len(p.opts.KubeconfigGlobs) == 0 {
		p.opts.KubeconfigGlobs = DefaultKubeconfigGlobs
	}

	if p.opts.Separator == "" {
		p.opts.Separator = "+"
	}

	p.log = log.Log.WithName("file-cluster-provider")
	p.Clusters = clusters.New[cluster.Cluster]()
	p.Clusters.ErrorHandler = p.log.Error

	p.log.Info("file cluster provider initialized",
		"kubeconfigFiles", p.opts.KubeconfigFiles,
		"kubeconfigDirs", p.opts.KubeconfigDirs,
		"kubeconfigGlobs", p.opts.KubeconfigGlobs,
	)

	return p, nil
}

// Provider is a multicluster.Provider that loads clusters from
// kubeconfig files or directories on disk.
type Provider struct {
	clusters.Clusters[cluster.Cluster]

	opts Options
	log  logr.Logger
}

// Start starts the provider and updates the clusters and is blocking.
func (p *Provider) Start(ctx context.Context, mcAware multicluster.Aware) error {
	if err := p.run(ctx, mcAware); err != nil {
		return fmt.Errorf("initial update failed: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer watcher.Close()

	for _, file := range p.opts.KubeconfigFiles {
		// Watching the directory instead of kubeconfig files as
		// watching non-existing files is not supported in fsnotify.
		if err := watcher.Add(filepath.Dir(file)); err != nil {
			return fmt.Errorf("failed to watch parent dir of kubeconfig file %q: %w", file, err)
		}
	}

	for _, dir := range p.opts.KubeconfigDirs {
		if err := watcher.Add(dir); err != nil {
			return fmt.Errorf("failed to watch kubeconfig directory %q: %w", dir, err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.Events:
			if !ok {
				return fmt.Errorf("file watcher closed")
			}
			p.log.Info("received fsnotify event", "event", event)
			// Only updating from a single file would be possible but
			// would also require to track which cluster belongs to
			// which file.
			// Instead clusters are just updated from all files.
			if err := p.run(ctx, mcAware); err != nil {
				p.log.Error(err, "failed to update clusters after file change")
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return fmt.Errorf("file watcher errors channel closed")
			}
			p.log.Error(err, "file watcher error")
		}
	}
}

// RunOnce performs a single update of the clusters.
func (p *Provider) RunOnce(ctx context.Context, mcAware multicluster.Aware) error {
	return p.run(ctx, mcAware)
}

func (p *Provider) run(ctx context.Context, mcAware multicluster.Aware) error {
	loadedClusters, err := p.loadClusters()
	if err != nil {
		return fmt.Errorf("failed to load clusters: %w", err)
	}
	knownClusters := p.Clusters.ClusterNames()

	// add new clusters
	for clusterName, cl := range loadedClusters {
		p.log.Info("adding or updating cluster", "clusterName", clusterName)
		if err := p.Clusters.AddOrReplace(ctx, clusterName, cl, mcAware); err != nil {
			p.log.Error(err, "failed to add or replace cluster", "clusterName", clusterName)
		}
	}

	// delete clusters that are no longer present
	for _, clusterName := range knownClusters {
		if _, ok := loadedClusters[clusterName]; ok {
			continue
		}
		p.log.Info("removing cluster", "clusterName", clusterName)
		p.Clusters.Remove(clusterName)
	}

	return nil
}

// Get returns the cluster with the given name.
// If the cluster name is empty (""), it returns the first cluster
// found.
func (p *Provider) Get(ctx context.Context, clusterName string) (cluster.Cluster, error) {
	if clusterName == "" {
		clusterNames := p.Clusters.ClusterNames()
		if len(clusterNames) == 0 {
			return nil, multicluster.ErrClusterNotFound
		}
		return p.Clusters.Get(ctx, clusterNames[0])
	}
	return p.Clusters.Get(ctx, clusterName)
}
