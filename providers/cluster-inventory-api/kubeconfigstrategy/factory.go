package kubeconfigstrategy

import (
	"context"
	"fmt"
)

// Option specifies which strategy will be applied to fetch the kubeconfig from ClusterProfile.
// Exactly one of Secret, AccessProvider, or CredentialsProvider must be set.
type Option struct {
	// Secret specifies option for the Secret strategy
	Secret *SecretStrategyOption

	// AccessProvider specifies option for AccessProvider strategy.
	AccessProvider *AccessProviderOption

	// CredentialsProvider specifies option for the deprecated CredentialsProvider strategy.
	//
	// Deprecated: Use AccessProvider instead.
	CredentialsProvider *CredentialsProviderOption
}

// New creates a new kubeconfig strategy based on the provided options.
func New(ctx context.Context, option Option) (Interface, error) {
	accessProvider := option.AccessProvider
	if option.CredentialsProvider != nil {
		if accessProvider != nil {
			return nil, fmt.Errorf("only one of AccessProvider or CredentialsProvider can be provided")
		}
		accessProvider = option.CredentialsProvider
	}

	if accessProvider == nil && option.Secret == nil {
		return nil, fmt.Errorf("either AccessProvider or Secret must be provided")
	}
	if accessProvider != nil && option.Secret != nil {
		return nil, fmt.Errorf("only one of AccessProvider or Secret can be provided")
	}

	if option.Secret != nil {
		return newSecretKubeConfigStrategy(ctx, *option.Secret)
	}
	return newAccessProviderStrategy(ctx, *accessProvider)
}
