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

package multicluster

import (
	"errors"
)

var (
	// ErrClusterNotFound can be returned by provider implementations if the cluster requested
	// doesn't exist and cannot be constructed.
	ErrClusterNotFound = errClusterNotFound()

	// ErrClusterNotOwned is returned by Manager.GetCluster when the named
	// cluster is known to the provider but this process has not been
	// granted ownership of it by the configured Coordinator. Callers should
	// treat this as a normal, expected condition — not an error worth
	// logging or retrying aggressively — since another process owns the
	// cluster and is responsible for it. Ownership may be granted later
	// (e.g. after a rehash or a lease change).
	ErrClusterNotOwned = errClusterNotOwned()
)

func errClusterNotFound() error { return errors.New("cluster not found") }

func errClusterNotOwned() error { return errors.New("cluster is not owned by this process") }
