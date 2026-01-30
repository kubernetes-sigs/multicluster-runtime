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

package cache

import (
	"context"
	"net/http"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/tools/record"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

// ScopedCluster implements cluster.Cluster using a ScopedCache.
// It provides a filtered view of a wildcard cache for a single cluster,
// while maintaining the full cluster.Cluster interface.
type ScopedCluster struct {
	name       string
	config     *rest.Config
	scheme     *runtime.Scheme
	cache      cache.Cache
	client     client.Client
	mapper     meta.RESTMapper
	httpClient *http.Client
}

// NewScopedCluster creates a new ScopedCluster that provides a filtered view
// of the wildcard cache for the specified cluster.
func NewScopedCluster(name string, cfg *rest.Config, wildcardCache WildcardCache, scheme *runtime.Scheme) (cluster.Cluster, error) {
	scopedCa := NewScopedCache(wildcardCache, name, scheme)

	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return nil, err
	}

	mapper, err := apiutil.NewDynamicRESTMapper(cfg, httpClient)
	if err != nil {
		return nil, err
	}

	c, err := client.New(cfg, client.Options{
		Scheme: scheme,
		Cache:  &client.CacheOptions{Reader: scopedCa},
	})
	if err != nil {
		return nil, err
	}

	return &ScopedCluster{
		name:       name,
		config:     cfg,
		scheme:     scheme,
		cache:      scopedCa,
		client:     c,
		mapper:     mapper,
		httpClient: httpClient,
	}, nil
}

// GetHTTPClient returns an HTTP client for the cluster.
func (c *ScopedCluster) GetHTTPClient() *http.Client {
	return c.httpClient
}

// GetConfig returns the rest.Config for the cluster.
func (c *ScopedCluster) GetConfig() *rest.Config {
	return c.config
}

// GetScheme returns the scheme used by the cluster.
func (c *ScopedCluster) GetScheme() *runtime.Scheme {
	return c.scheme
}

// GetClient returns a client for the cluster.
func (c *ScopedCluster) GetClient() client.Client {
	return c.client
}

// GetCache returns the cache for the cluster.
func (c *ScopedCluster) GetCache() cache.Cache {
	return c.cache
}

// GetFieldIndexer returns a field indexer for the cluster.
func (c *ScopedCluster) GetFieldIndexer() client.FieldIndexer {
	return c.cache
}

// GetRESTMapper returns the REST mapper for the cluster.
func (c *ScopedCluster) GetRESTMapper() meta.RESTMapper {
	return c.mapper
}

// GetAPIReader returns a reader that bypasses the cache.
// For scoped clusters, this returns the scoped cache as the reader.
func (c *ScopedCluster) GetAPIReader() client.Reader {
	return c.cache
}

// GetEventRecorderFor returns an event recorder for the given name.
//
// Deprecated: this uses the old events API. Use GetEventRecorder instead.
func (c *ScopedCluster) GetEventRecorderFor(name string) record.EventRecorder {
	// Return a fake recorder for now; a real implementation would
	// create a recorder that targets events to the correct cluster.
	return record.NewFakeRecorder(100)
}

// GetEventRecorder returns an event recorder for the given name.
func (c *ScopedCluster) GetEventRecorder(name string) events.EventRecorder {
	// Return a fake recorder for now; a real implementation would
	// create a recorder that targets events to the correct cluster.
	return &fakeEventsRecorder{}
}

// Start starts the cluster. For scoped clusters, this is a no-op
// because the underlying wildcard cache is started separately.
func (c *ScopedCluster) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// fakeEventsRecorder is a fake events.EventRecorder that does nothing.
type fakeEventsRecorder struct{}

func (f *fakeEventsRecorder) Eventf(regarding, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
}
