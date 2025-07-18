package file

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

type clusters map[string]cluster.Cluster

func (p *Provider) loadClusters() (clusters, error) {
	filepaths, err := p.collectPaths(p.opts.Paths)
	if err != nil {
		return nil, err
	}

	kubeCtxs := map[string]*rest.Config{}
	for _, filepath := range filepaths {
		fileKubeCtxs, err := readFile(filepath)
		if err != nil {
			p.log.Error(err, "failed to read kubeconfig file", "file", filepath)
			continue
		}
		for name, kubeCtx := range fileKubeCtxs {
			if _, exists := kubeCtxs[name]; exists {
				p.log.Error(nil, "duplicate context name found", "context", name, "file", filepath)
				continue
			}
			kubeCtxs[name] = kubeCtx
		}
	}

	return p.fromContexts(kubeCtxs), nil
}

func (p *Provider) collectPaths(paths []string) ([]string, error) {
	filepaths := make([]string, 0, len(paths))

	for _, path := range paths {
		if path == "" {
			continue
		}

		stat, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				p.log.Info("path does not exist, skipping", "path", path)
				continue
			}
			return nil, fmt.Errorf("failed to stat path %q: %w", path, err)
		}

		if !stat.IsDir() {
			filepaths = append(filepaths, path)
			continue
		}

		filepaths = append(filepaths, p.matchKubeconfigGlobs(path)...)
	}

	return filepaths, nil
}

func (p *Provider) matchKubeconfigGlobs(dirpath string) []string {
	matches := []string{}

	for _, glob := range p.opts.KubeconfigGlobs {
		globMatches, err := filepath.Glob(filepath.Join(dirpath, glob))
		if err != nil {
			p.log.Error(err, "failed to glob files", "dirpath", dirpath, "glob", glob)
			continue
		}
		matches = append(matches, globMatches...)
	}

	return matches
}

func readFile(filepath string) (map[string]*rest.Config, error) {
	config, err := clientcmd.LoadFromFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig from file %q: %w", filepath, err)
	}

	ret := make(map[string]*rest.Config, len(config.Contexts))
	for name := range config.Contexts {
		restConfig, err := clientcmd.NewNonInteractiveClientConfig(*config, name, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to create rest config for context %q: %w", name, err)
		}
		ret[name] = restConfig
	}

	return ret, nil
}

func (p *Provider) fromContexts(kubeCtxs map[string]*rest.Config) clusters {
	c := make(map[string]cluster.Cluster, len(kubeCtxs))

	for name, kubeCtx := range kubeCtxs {
		cl, err := cluster.New(kubeCtx)
		if err != nil {
			p.log.Error(err, "failed to create cluster", "context", name)
			continue
		}
		if _, ok := c[name]; ok {
			p.log.Error(nil, "duplicate context name found", "context", name)
			continue
		}
		c[name] = cl
	}

	return c
}
