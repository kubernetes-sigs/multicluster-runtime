package kubeconfigstrategy

import (
	"context"

	"k8s.io/client-go/rest"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/cluster-inventory-api/pkg/credentials"
)

var _ Interface = &credentialsProviderStrategy{}

// CredentialsProviderOption specifies the credentials provider option.
// It contains the credentials provider that will be used to build the kubeconfig.
type CredentialsProviderOption struct {
	Provider *credentials.CredentialsProvider
}

type credentialsProviderStrategy struct {
	provider *credentials.CredentialsProvider
}

func newCredentialsProviderStrategy(ctx context.Context, option CredentialsProviderOption) (Interface, error) {
	log.FromContext(ctx).Info("Using CredentialsProvider strategy for fetching kubeconfig from ClusterProfile")
	return &credentialsProviderStrategy{
		provider: option.Provider,
	}, nil
}

// CustomWatches implements Interface.
func (c *credentialsProviderStrategy) CustomWatches() []CustomWatch {
	return nil
}

// GetKubeConfig implements Interface.
func (c *credentialsProviderStrategy) GetKubeConfig(ctx context.Context, cli client.Client, clp *v1alpha1.ClusterProfile) (*rest.Config, error) {
	return c.provider.BuildConfigFromCP(clp)
}
