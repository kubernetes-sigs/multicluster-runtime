package kubeconfigstrategy

import (
	"context"

	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"

	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Interface defines how the kubeconfig for a cluster profile is managed.
// It is used to fetch the kubeconfig for a cluster profile.
type Interface interface {
	// GetKubeConfig is a function that returns the kubeconfig secret for a cluster profile.
	GetKubeConfig(ctx context.Context, cli client.Client, clp *clusterinventoryv1alpha1.ClusterProfile) (*rest.Config, error)

	// CustomWatches can add custom watches to the provider controller
	CustomWatches() []CustomWatch
}

// CustomWatch specifies a custom watch spec that can be added to the provider controller.
type CustomWatch struct {
	Object       client.Object
	EventHandler handler.TypedEventHandler[client.Object, reconcile.Request]
	Opts         []builder.WatchesOption
}
