package kubeconfigstrategy

import (
	"context"

	"sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/access"

	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var _ Interface = &accessProviderStrategy{}

// AccessProviderOption specifies the access provider option.
// It contains the access config that will be used to build the kubeconfig.
type AccessProviderOption struct {
	Provider *access.Config
}

// CredentialsProviderOption specifies the deprecated credentials provider option.
//
// Deprecated: Use AccessProviderOption instead.
type CredentialsProviderOption = AccessProviderOption

type accessProviderStrategy struct {
	provider *access.Config
}

func newAccessProviderStrategy(ctx context.Context, option AccessProviderOption) (Interface, error) {
	log.FromContext(ctx).Info("Using AccessProvider strategy for fetching kubeconfig from ClusterProfile")
	return &accessProviderStrategy{
		provider: option.Provider,
	}, nil
}

// CustomWatches implements Interface.
func (c *accessProviderStrategy) CustomWatches() []CustomWatch {
	return nil
}

// GetKubeConfig implements Interface.
func (c *accessProviderStrategy) GetKubeConfig(ctx context.Context, cli client.Client, clp *v1alpha1.ClusterProfile) (*rest.Config, error) {
	return c.provider.BuildConfigFromCP(clp)
}
