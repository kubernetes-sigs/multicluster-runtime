#!/usr/bin/env bash

# Copyright The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

HUB_CLUSTER="${HUB_CLUSTER:-demo-hub}"
SPOKE_CLUSTER="${SPOKE_CLUSTER:-demo-spoke}"
CONSUMER_NAMESPACE="${CONSUMER_NAMESPACE:-spoke-manager}"
PROFILE_NAMESPACE="${PROFILE_NAMESPACE:-fleet}"
PROFILE_NAME="${PROFILE_NAME:-spoke-1}"
APP_NAME="${APP_NAME:-cluster-inventory-api-example}"
CONTROLLER_IMAGE="${CONTROLLER_IMAGE:-localhost/cluster-inventory-api-example:e2e}"
KUBECONFIG_SECRETREADER_IMAGE="registry.k8s.io/cluster-inventory-api/kubeconfig-secretreader:v0.1.2"
CONFIGMAP_NAMESPACE="${CONFIGMAP_NAMESPACE:-default}"
CONFIGMAP_NAME="${CONFIGMAP_NAME:-cluster-inventory-api-e2e}"
GO_VERSION="${GO_VERSION:-$(make -C "${REPO_ROOT}" go-version)}"

cd "${REPO_ROOT}"

echo "--- Building controller image ${CONTROLLER_IMAGE}"
docker buildx build \
  -f examples/cluster-inventory-api/Dockerfile \
  --build-arg "GO_VERSION=${GO_VERSION}" \
  -t "${CONTROLLER_IMAGE}" \
  --load \
  .
kind load docker-image "${CONTROLLER_IMAGE}" --name "${HUB_CLUSTER}"

echo "--- Patching ClusterProfile server for in-kind access"
SPOKE_HOST="${SPOKE_CLUSTER}-control-plane"
kubectl --context "kind-${HUB_CLUSTER}" patch clusterprofile "${PROFILE_NAME}" \
  --namespace "${PROFILE_NAMESPACE}" \
  --type=json \
  --subresource=status \
  -p "[{\"op\":\"replace\",\"path\":\"/status/accessProviders/0/cluster/server\",\"value\":\"https://${SPOKE_HOST}:6443\"}]"

SPOKE_IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${SPOKE_HOST}")"
if [[ -z "${SPOKE_IP}" ]]; then
  echo "ERROR: could not resolve ${SPOKE_HOST} container IP" >&2
  exit 1
fi

echo "--- Applying hub RBAC"
kubectl --context "kind-${HUB_CLUSTER}" apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${APP_NAME}
  namespace: ${CONSUMER_NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ${APP_NAME}
  namespace: ${CONSUMER_NAMESPACE}
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${APP_NAME}
  namespace: ${CONSUMER_NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ${APP_NAME}
subjects:
  - kind: ServiceAccount
    name: ${APP_NAME}
    namespace: ${CONSUMER_NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${APP_NAME}-clusterprofile-reader
rules:
  - apiGroups: ["multicluster.x-k8s.io"]
    resources: ["clusterprofiles"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${APP_NAME}-clusterprofile-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ${APP_NAME}-clusterprofile-reader
subjects:
  - kind: ServiceAccount
    name: ${APP_NAME}
    namespace: ${CONSUMER_NAMESPACE}
EOF

echo "--- Creating provider config"
kubectl --context "kind-${HUB_CLUSTER}" create configmap "${APP_NAME}-provider-config" \
  --namespace "${CONSUMER_NAMESPACE}" \
  --from-file=clusterprofile-provider-file.json="${SCRIPT_DIR}/clusterprofile-provider-file.json" \
  --dry-run=client -o yaml | kubectl --context "kind-${HUB_CLUSTER}" apply -f -

echo "--- Deploying controller"
kubectl --context "kind-${HUB_CLUSTER}" delete deployment "${APP_NAME}" \
  --namespace "${CONSUMER_NAMESPACE}" \
  --ignore-not-found
kubectl --context "kind-${HUB_CLUSTER}" apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${APP_NAME}
  namespace: ${CONSUMER_NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ${APP_NAME}
  template:
    metadata:
      labels:
        app: ${APP_NAME}
    spec:
      serviceAccountName: ${APP_NAME}
      terminationGracePeriodSeconds: 5
      hostAliases:
        - hostnames: ["${SPOKE_HOST}"]
          ip: "${SPOKE_IP}"
      containers:
        - name: controller
          image: ${CONTROLLER_IMAGE}
          imagePullPolicy: Never
          args:
            - -clusterprofile-provider-file=/config/clusterprofile-provider-file.json
          volumeMounts:
            - name: plugin-volume
              mountPath: /plugin
              readOnly: true
            - name: provider-config
              mountPath: /config
              readOnly: true
      volumes:
        - name: plugin-volume
          image:
            reference: ${KUBECONFIG_SECRETREADER_IMAGE}
            pullPolicy: IfNotPresent
        - name: provider-config
          configMap:
            name: ${APP_NAME}-provider-config
            items:
              - key: clusterprofile-provider-file.json
                path: clusterprofile-provider-file.json
EOF

kubectl --context "kind-${HUB_CLUSTER}" rollout status "deployment/${APP_NAME}" \
  --namespace "${CONSUMER_NAMESPACE}" \
  --timeout=120s

echo "--- Creating spoke ConfigMap"
kubectl --context "kind-${SPOKE_CLUSTER}" create configmap "${CONFIGMAP_NAME}" \
  --namespace "${CONFIGMAP_NAMESPACE}" \
  --from-literal=source=e2e \
  --dry-run=client -o yaml | kubectl --context "kind-${SPOKE_CLUSTER}" apply -f -
kubectl --context "kind-${SPOKE_CLUSTER}" label configmap "${CONFIGMAP_NAME}" \
  --namespace "${CONFIGMAP_NAMESPACE}" \
  cluster-inventory-api-e2e-run="$(date +%s)" \
  --overwrite

echo "--- Waiting for controller logs"
EXPECTED_CONFIGMAP="${CONFIGMAP_NAMESPACE}/${CONFIGMAP_NAME}"
LOGS=""
POD=""
logs_contain() {
  grep -Fq -- "$1" <<<"${LOGS}"
}
for _ in $(seq 1 90); do
  POD="$(kubectl --context "kind-${HUB_CLUSTER}" get pods \
    --namespace "${CONSUMER_NAMESPACE}" \
    -l "app=${APP_NAME}" \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [[ -n "${POD}" ]]; then
    LOGS="$(kubectl --context "kind-${HUB_CLUSTER}" logs "${POD}" \
      --namespace "${CONSUMER_NAMESPACE}" 2>&1 || true)"
    if logs_contain "Cluster engaged manager for ClusterProfile" && \
      logs_contain "${PROFILE_NAMESPACE}/${PROFILE_NAME}" && \
      logs_contain "ConfigMap in cluster" && \
      logs_contain "${EXPECTED_CONFIGMAP}"; then
      echo "${LOGS}"
      echo "--- Cluster Inventory API example e2e passed"
      exit 0
    fi
  fi
  sleep 2
done

echo "ERROR: timed out waiting for controller to reconcile ${EXPECTED_CONFIGMAP}" >&2
if [[ -n "${POD}" ]]; then
  kubectl --context "kind-${HUB_CLUSTER}" describe pod "${POD}" --namespace "${CONSUMER_NAMESPACE}" >&2 || true
  echo "${LOGS}" >&2
else
  kubectl --context "kind-${HUB_CLUSTER}" get pods --namespace "${CONSUMER_NAMESPACE}" >&2 || true
fi
exit 1
