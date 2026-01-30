/*
Copyright 2026 The Kubernetes Authors.

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

package cache

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/cache"
)

const (
	// ClusterAnnotation is the annotation key used to store the cluster name on objects.
	ClusterAnnotation = "multicluster.x-k8s.io/cluster-name"

	// ClusterIndexName is the name of the index that indexes objects by cluster.
	ClusterIndexName = "cluster"

	// ClusterSeparator is the separator used between cluster name and namespace/name in keys.
	ClusterSeparator = "|"
)

// DeletionHandlingMetaClusterNamespaceKeyFunc checks for
// DeletedFinalStateUnknown objects before calling
// MetaClusterNamespaceKeyFunc.
func DeletionHandlingMetaClusterNamespaceKeyFunc(obj interface{}) (string, error) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return d.Key, nil
	}
	return MetaClusterNamespaceKeyFunc(obj)
}

// MetaClusterNamespaceKeyFunc is a convenient default KeyFunc which knows how to make
// keys for API objects which implement meta.Interface.
// The key uses the format <clusterName>|<namespace>/<name> unless <namespace> is empty, then
// it's just <clusterName>|<name>, and if no cluster annotation is present,
// it's just <namespace>/<name> or <name>.
func MetaClusterNamespaceKeyFunc(obj interface{}) (string, error) {
	if key, ok := obj.(cache.ExplicitKey); ok {
		return string(key), nil
	}
	m, err := meta.Accessor(obj)
	if err != nil {
		return "", fmt.Errorf("object has no meta: %w", err)
	}

	cluster := ""
	if annotations := m.GetAnnotations(); annotations != nil {
		cluster = annotations[ClusterAnnotation]
	}

	return ToClusterAwareKey(cluster, m.GetNamespace(), m.GetName()), nil
}

// ToClusterAwareKey formats a cluster, namespace, and name as a key.
// Format: <cluster>|<namespace>/<name> or <cluster>|<name> if namespace is empty.
// If cluster is empty, falls back to standard namespace/name format.
func ToClusterAwareKey(cluster, namespace, name string) string {
	var key string
	if cluster != "" {
		key += cluster + ClusterSeparator
	}
	if namespace != "" {
		key += namespace + "/"
	}
	key += name
	return key
}

// SplitMetaClusterNamespaceKey returns the cluster, namespace and name that
// MetaClusterNamespaceKeyFunc encoded into key.
func SplitMetaClusterNamespaceKey(key string) (clusterName, namespace, name string, err error) {
	invalidKey := fmt.Errorf("unexpected key format: %q", key)
	outerParts := strings.Split(key, ClusterSeparator)
	switch len(outerParts) {
	case 1:
		// No cluster separator found, use standard namespace/name parsing
		namespace, name, err = cache.SplitMetaNamespaceKey(outerParts[0])
		if err != nil {
			err = invalidKey
		}
		return "", namespace, name, err
	case 2:
		// Cluster separator found
		namespace, name, err = cache.SplitMetaNamespaceKey(outerParts[1])
		if err != nil {
			err = invalidKey
		}
		return outerParts[0], namespace, name, err
	default:
		return "", "", "", invalidKey
	}
}

// ClusterIndexFunc is an index function that indexes objects by their cluster annotation.
func ClusterIndexFunc(obj interface{}) ([]string, error) {
	m, err := meta.Accessor(obj)
	if err != nil {
		return nil, fmt.Errorf("object has no meta: %w", err)
	}

	cluster := ""
	if annotations := m.GetAnnotations(); annotations != nil {
		cluster = annotations[ClusterAnnotation]
	}

	if cluster == "" {
		return []string{}, nil
	}
	return []string{cluster}, nil
}
