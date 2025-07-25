package kubeconfigstrategy

import (
	"context"
	"fmt"

	"sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// SecretLabelKeyClusterInventoryConsumer is the label key used to identify the consumer of the kubeconfig Secret.
	SecretLabelKeyClusterInventoryConsumer = "x-k8s.io/cluster-inventory-consumer"
	// SecretLabelKeyClusterProfile is the label key used to identify the ClusterProfile associated with the kubeconfig Secret.
	SecretLabelKeyClusterProfile = "x-k8s.io/cluster-profile"
	// SecretDataKeyKubeConfig is the key in the Secret data that contains the kubeconfig.
	SecretDataKeyKubeConfig = "Config"
)

var _ Interface = &secretStrategy{}

type secretStrategy struct {
	consumerName string
}

// SecretStrategyOption holds options for the Secret strategy.
type SecretStrategyOption struct {
	ConsumerName string
}

func newSecretKubeConfigStrategy(ctx context.Context, option SecretStrategyOption) (Interface, error) {
	if option.ConsumerName == "" {
		return nil, fmt.Errorf("consumer name must be set for Secret strategy")
	}
	log.FromContext(ctx).Info("Using Secret strategy for  for fetching kubeconfig from ClusterProfile", "consumerName", option.ConsumerName)
	return &secretStrategy{
		consumerName: option.ConsumerName,
	}, nil
}

// CustomWatches implements Interface.
func (s *secretStrategy) CustomWatches() []CustomWatch {
	return []CustomWatch{{
		Object: &corev1.Secret{},
		EventHandler: handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				return nil
			}

			if secret.GetLabels() == nil ||
				secret.GetLabels()[SecretLabelKeyClusterInventoryConsumer] != s.consumerName ||
				secret.GetLabels()[SecretLabelKeyClusterProfile] == "" {
				return nil
			}

			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Namespace: secret.GetNamespace(),
					Name:      secret.GetLabels()[SecretLabelKeyClusterProfile],
				},
			}}
		}),
		Opts: []builder.WatchesOption{
			builder.WithPredicates(predicate.NewPredicateFuncs(func(object client.Object) bool {
				secret, ok := object.(*corev1.Secret)
				if !ok {
					return false
				}
				return secret.GetLabels()[SecretLabelKeyClusterInventoryConsumer] == s.consumerName &&
					secret.GetLabels()[SecretLabelKeyClusterProfile] != ""
			})),
		},
	}}
}

// GetKubeConfig implements Interface.
func (s *secretStrategy) GetKubeConfig(ctx context.Context, cli client.Client, clp *v1alpha1.ClusterProfile) (*rest.Config, error) {
	secrets := corev1.SecretList{}
	if err := cli.List(ctx, &secrets, client.InNamespace(clp.Namespace), client.MatchingLabels{
		SecretLabelKeyClusterInventoryConsumer: s.consumerName,
		SecretLabelKeyClusterProfile:           clp.Name,
	}); err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}

	if len(secrets.Items) == 0 {
		return nil, fmt.Errorf("no secrets found")
	}

	if len(secrets.Items) > 1 {
		return nil, fmt.Errorf("multiple secrets found, expected one, got %d", len(secrets.Items))
	}

	secret := secrets.Items[0]

	data, ok := secret.Data[SecretDataKeyKubeConfig]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s does not contain Config data", secret.Namespace, secret.Name)
	}
	return clientcmd.RESTConfigFromKubeConfig(data)
}
