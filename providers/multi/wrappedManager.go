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
