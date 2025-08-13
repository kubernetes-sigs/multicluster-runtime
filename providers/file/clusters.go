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
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

type clusters map[string]cluster.Cluster

func (p *Provider) loadClusters() (clusters, error) {
	filepaths, err := p.collectPaths()
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
			clusterName := filepath + p.opts.Separator + name
			if _, exists := kubeCtxs[clusterName]; exists {
				p.log.Error(nil, "duplicate context name found", "file", filepath, "context", name, "clusterName", clusterName)
				continue
			}
			kubeCtxs[clusterName] = kubeCtx
		}
	}

	return p.fromContexts(kubeCtxs), nil
}

func (p *Provider) collectPaths() ([]string, error) {
	filepaths := []string{}

	for _, file := range p.opts.KubeconfigFiles {
		if _, err := os.Stat(file); err != nil {
			if os.IsNotExist(err) {
				p.log.Info("kubeconfig file does not exist, skipping", "file", file)
				continue
			}
			return nil, fmt.Errorf("failed to stat kubeconfig file %q: %w", file, err)
		}
		filepaths = append(filepaths, file)
	}

	for _, dir := range p.opts.KubeconfigDirs {
		if _, err := os.Stat(dir); err != nil {
			if os.IsNotExist(err) {
				p.log.Info("directory does not exist, skipping", "dir", dir)
				continue
			}
			return nil, fmt.Errorf("failed to stat directory %q: %w", dir, err)
		}

		filepaths = append(filepaths, p.matchKubeconfigGlobs(dir)...)
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
