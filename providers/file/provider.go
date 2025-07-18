package file

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/util/wait"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var _ multicluster.Provider = &Provider{}

// Options defines the options for the file-based cluster provider.
type Options struct {
	// UpdateInterval is the interval at which the provider will poll
	// for changes in the kubeconfig files and directories.
	// Default is one second.
	UpdateInterval time.Duration

	// Paths can be either paths to kubeconfig files or directories.
	// If a directory is specified, the provider will look for files
	// matching the KubeconfigGlobs.
	// The provider will collect all kubeconfig files from all paths.
	// The default is:
	// - KUBECONFIG if it is set
	// - ~/.kube/config if it exists
	// - the current directory otherwise.
	// Default is the current directory.
	Paths []string

	// KubeconfigGlobs are the glob patterns to match kubeconfig files
	// in directories.
	// Default is DefaultKubeconfigGlobs.
	KubeconfigGlobs []string
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

func defaultKubeconfigPaths() []string {
	if envKubeconfig := os.Getenv("KUBECONFIG"); envKubeconfig != "" {
		return []string{envKubeconfig}
	}

	if fstat, err := os.Stat(os.ExpandEnv("$HOME/.kube/config")); err == nil && !fstat.IsDir() {
		return []string{os.ExpandEnv("$HOME/.kube/config")}
	}

	return []string{"."}
}

// New returns a new Provider with the given options.
func New(opts Options) (*Provider, error) {
	p := new(Provider)
	p.opts = opts

	if p.opts.UpdateInterval == 0 {
		p.opts.UpdateInterval = 1 * time.Second
	}

	if len(p.opts.Paths) == 0 {
		p.opts.Paths = defaultKubeconfigPaths()
	}

	if len(p.opts.KubeconfigGlobs) == 0 {
		p.opts.KubeconfigGlobs = DefaultKubeconfigGlobs
	}

	p.log = log.Log.WithName("file-cluster-provider")
	p.clusters = make(map[string]cluster.Cluster)
	p.clusterCancel = make(map[string]func())

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
func (p *Provider) Run(ctx context.Context) error {
	if err := p.run(ctx); err != nil {
		return fmt.Errorf("initial update failed: %w", err)
	}
	return wait.PollUntilContextCancel(ctx, p.opts.UpdateInterval, true, func(ctx context.Context) (done bool, err error) {
		if err := p.run(ctx); err != nil {
			p.log.Error(err, "failed to update clusters")
		}
		return false, nil
	})
}

// RunOnce performs a single update of the clusters.
func (p *Provider) RunOnce(ctx context.Context) error {
	return p.run(ctx)
}

func (p *Provider) addCluster(ctx context.Context, name string, cl cluster.Cluster) {
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

func (p *Provider) run(ctx context.Context) error {
	currentClusters, err := p.loadClusters()
	if err != nil {
		return fmt.Errorf("failed to load clusters: %w", err)
	}
	knownClusters := p.ClusterNames()

	// add new clusters
	for name, cl := range currentClusters {
		if slices.Contains(knownClusters, name) {
			continue
		}
		p.addCluster(ctx, name, cl)
	}

	// delete clusters that are no longer present
	for _, name := range knownClusters {
		if _, ok := currentClusters[name]; ok {
			continue
		}
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
