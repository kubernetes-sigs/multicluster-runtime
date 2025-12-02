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

package acceptance

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func getClient(t testing.TB, cfg *rest.Config) kubernetes.Interface {
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}
	return client
}

func writeConfigMap(t testing.TB, cfg *rest.Config, name, data string) {
	client := getClient(t, cfg).CoreV1().ConfigMaps("default")

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Data: map[string]string{
			"data": data,
		},
	}

	if _, err := client.Create(t.Context(), cm, metav1.CreateOptions{}); err != nil {
		t.Fatalf("Failed to create ConfigMap: %v", err)
	}
}

func getConfigMap(t testing.TB, cfg *rest.Config, name string) string {
	client := getClient(t, cfg).CoreV1().ConfigMaps("default")

	cm, err := client.Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get ConfigMap: %v", err)
	}
	return cm.Data["data"]
}
