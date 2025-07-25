package kubeconfigstrategy

import (
	"context"
	"fmt"
)

// Option specifies which strategy will be applied to fetch the kubeconfig from ClusterProfile.
// Either Secret or CredentialsProvider must be set, but not both.
type Option struct {
	// Secret specifies option for the Secret strategy
	Secret *SecretStrategyOption

	// CredentialsProvider specifies option for CredentialsProvider strategy.
	CredentialsProvider *CredentialsProviderOption
}

// New creates a new kubeconfig strategy based on the provided options.
func New(ctx context.Context, option Option) (Interface, error) {
	if option.CredentialsProvider == nil && option.Secret == nil {
		return nil, fmt.Errorf("either CredentialsProvider or Secret must be provided")
	}
	if option.CredentialsProvider != nil && option.Secret != nil {
		return nil, fmt.Errorf("only one of CredentialsProvider or Secret can be provided")
	}

	if option.Secret != nil {
		return newSecretKubeConfigStrategy(ctx, *option.Secret)
	}
	return newCredentialsProviderStrategy(ctx, *option.CredentialsProvider)
}
