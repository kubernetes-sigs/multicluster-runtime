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

package multi

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/cluster"

	mctrl "sigs.k8s.io/multicluster-runtime"
)

var _ mctrl.Manager = &wrappedManager{}

type wrappedManager struct {
	mctrl.Manager
	prefix, sep string
}

func (w *wrappedManager) Engage(ctx context.Context, name string, cl cluster.Cluster) error {
	return w.Manager.Engage(ctx, w.prefix+w.sep+name, cl)
}
