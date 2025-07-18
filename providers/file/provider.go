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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"gopkg.in/fsnotify.v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}

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
	p.clusters = make(map[string]cluster.Cluster)
	p.clusterCancel = make(map[string]func())

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
	opts Options

	log logr.Logger

	clustersLock  sync.RWMutex
	clusters      map[string]cluster.Cluster
	clusterCancel map[string]func()
}

// Run starts the provider and updates the clusters and is blocking.
func (p *Provider) Run(ctx context.Context, mgr mcmanager.Manager) error {
	if err := p.run(ctx, mgr); err != nil {
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
			if err := p.run(ctx, mgr); err != nil {
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
func (p *Provider) RunOnce(ctx context.Context, mgr mcmanager.Manager) error {
	return p.run(ctx, mgr)
}

func (p *Provider) addCluster(ctx context.Context, mgr mcmanager.Manager, name string, cl cluster.Cluster) {
	ctx, cancel := context.WithCancel(ctx)

	p.clustersLock.Lock()
	p.clusters[name] = cl
	p.clusterCancel[name] = cancel
	p.clustersLock.Unlock()

	go func() {
		if err := cl.Start(ctx); err != nil {
			p.log.Error(err, "error in cluster", "name", name)
		}
		p.removeCluster(name)
	}()

	if mgr != nil {
		if err := mgr.Engage(ctx, name, cl); err != nil {
			cancel()
			p.log.Error(err, "failed to engage cluster", "name", name)
		}
	}
}

func (p *Provider) removeCluster(name string) {
	p.clustersLock.Lock()
	defer p.clustersLock.Unlock()

	if cancel, ok := p.clusterCancel[name]; ok {
		cancel()
		delete(p.clusters, name)
		delete(p.clusterCancel, name)
	}
}

func (p *Provider) run(ctx context.Context, mgr mcmanager.Manager) error {
	loadedClusters, err := p.loadClusters()
	if err != nil {
		return fmt.Errorf("failed to load clusters: %w", err)
	}
	knownClusters := p.ClusterNames()

	// add new clusters
	for name, cl := range loadedClusters {
		if slices.Contains(knownClusters, name) {
			// update if the config has changed
			existingCluster, _ := p.Get(ctx, name)
			if !cmp.Equal(existingCluster.GetConfig(), cl.GetConfig()) {
				p.log.Info("updating cluster", "name", name)
				p.removeCluster(name)
				p.addCluster(ctx, mgr, name, cl)
			}
			continue
		}
		p.log.Info("adding cluster", "name", name)
		p.addCluster(ctx, mgr, name, cl)
	}

	// delete clusters that are no longer present
	for _, name := range knownClusters {
		if _, ok := loadedClusters[name]; ok {
			continue
		}
		p.log.Info("removing cluster", "name", name)
		p.removeCluster(name)
	}

	return nil
}

// Get returns the cluster with the given name.
// If the cluster name is empty (""), it returns the first cluster
// found.
func (p *Provider) Get(_ context.Context, clusterName string) (cluster.Cluster, error) {
	p.clustersLock.RLock()
	defer p.clustersLock.RUnlock()

	if clusterName == "" {
		for _, cl := range p.clusters {
			return cl, nil
		}
	}

	cl, ok := p.clusters[clusterName]
	if !ok {
		return nil, multicluster.ErrClusterNotFound
	}
	return cl, nil
}

// IndexField indexes a field on all clusters.
func (p *Provider) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	p.clustersLock.RLock()
	defer p.clustersLock.RUnlock()

	for name, cl := range p.clusters {
		if err := cl.GetCache().IndexField(ctx, obj, field, extractValue); err != nil {
			return fmt.Errorf("failed to index field %q on cluster %q: %w", field, name, err)
		}
	}
	return nil
}

// ClusterNames returns the names of all clusters known to the provider.
func (p *Provider) ClusterNames() []string {
	p.clustersLock.RLock()
	defer p.clustersLock.RUnlock()
	return slices.Sorted(maps.Keys(p.clusters))
}
