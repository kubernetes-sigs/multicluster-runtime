package kubeconfigstrategy

import (
	"context"
	"fmt"
)

// Option specifies which strategy will be applied to fetch the kubeconfig from ClusterProfile.
// Exactly one of AccessProvider or Secret must be set.
type Option struct {
	// Secret specifies option for the Secret strategy.
	//
	// Deprecated: Use AccessProvider instead.
	Secret *SecretStrategyOption

	// AccessProvider specifies option for AccessProvider strategy.
	AccessProvider *AccessProviderOption
}

// New creates a new kubeconfig strategy based on the provided options.
func New(ctx context.Context, option Option) (Interface, error) {
	count := 0
	if option.AccessProvider != nil {
		count++
	}
	if option.Secret != nil {
		count++
	}
	if count == 0 {
		return nil, fmt.Errorf("one of AccessProvider or Secret must be provided")
	}
	if count > 1 {
		return nil, fmt.Errorf("only one of AccessProvider or Secret can be provided")
	}

	if option.Secret != nil {
		return newSecretKubeConfigStrategy(ctx, *option.Secret)
	}
	return newAccessProviderStrategy(ctx, *option.AccessProvider)
}
