/*
Copyright 2026 The Kubernetes Authors.

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

package kubeconfigstrategy

import (
	"context"

	"sigs.k8s.io/cluster-inventory-api/pkg/access"

	clientauthenticationv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("New requires a provider",
	func(option Option) {
		strategy, err := New(context.Background(), option)

		Expect(err).To(MatchError(ContainSubstring("access provider must be set for AccessProvider strategy")))
		Expect(strategy).To(BeNil())
	},
	Entry("AccessProvider", Option{
		AccessProvider: &AccessProviderOption{},
	}),
	Entry("CredentialsProvider", Option{
		CredentialsProvider: &CredentialsProviderOption{},
	}),
)

var _ = DescribeTable("New creates a strategy when a provider is configured",
	func(option Option) {
		strategy, err := New(context.Background(), option)

		Expect(err).NotTo(HaveOccurred())
		Expect(strategy).NotTo(BeNil())
	},
	Entry("AccessProvider", Option{
		AccessProvider: &AccessProviderOption{Provider: validAccessConfig()},
	}),
	Entry("CredentialsProvider", Option{
		CredentialsProvider: &CredentialsProviderOption{Provider: validAccessConfig()},
	}),
)

func validAccessConfig() *access.Config {
	return access.New([]access.Provider{{
		Name: "test",
		ExecConfig: &clientcmdapi.ExecConfig{
			APIVersion: clientauthenticationv1.SchemeGroupVersion.String(),
			Command:    "true",
		},
	}})
}
