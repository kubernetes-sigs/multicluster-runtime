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

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

HUB_CLUSTER="${HUB_CLUSTER:-demo-hub}"
SPOKE_CLUSTER="${SPOKE_CLUSTER:-demo-spoke}"
CONSUMER_NAMESPACE="${CONSUMER_NAMESPACE:-spoke-manager}"
PROFILE_NAMESPACE="${PROFILE_NAMESPACE:-fleet}"
PROFILE_NAME="${PROFILE_NAME:-spoke-1}"
SPOKE_READER_NAMESPACE="${SPOKE_READER_NAMESPACE:-default}"
SPOKE_READER_SERVICE_ACCOUNT="${SPOKE_READER_SERVICE_ACCOUNT:-spoke-reader}"
SPOKE_READER_TOKEN_DURATION="${SPOKE_READER_TOKEN_DURATION:-24h}"

create_kind_cluster() {
  local name="$1"
  if kind get clusters | grep -qx "${name}"; then
    echo "--- Reusing kind cluster ${name}"
    return
  fi
  echo "--- Creating kind cluster ${name}"
  kind create cluster --name "${name}"
}

wait_for_cluster() {
  local context="$1"
  kubectl --context "${context}" wait --for=condition=Ready node --all --timeout=120s
}

create_kind_cluster "${SPOKE_CLUSTER}"
wait_for_cluster "kind-${SPOKE_CLUSTER}"

echo "--- Creating spoke ServiceAccount and RBAC"
kubectl --context "kind-${SPOKE_CLUSTER}" apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${SPOKE_READER_SERVICE_ACCOUNT}
  namespace: ${SPOKE_READER_NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-inventory-api-example-configmap-reader
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cluster-inventory-api-example-configmap-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: cluster-inventory-api-example-configmap-reader
subjects:
  - kind: ServiceAccount
    name: ${SPOKE_READER_SERVICE_ACCOUNT}
    namespace: ${SPOKE_READER_NAMESPACE}
EOF

echo "--- Creating spoke reader token"
TOKEN="$(kubectl --context "kind-${SPOKE_CLUSTER}" -n "${SPOKE_READER_NAMESPACE}" create token "${SPOKE_READER_SERVICE_ACCOUNT}" --duration="${SPOKE_READER_TOKEN_DURATION}")"
if [[ -z "${TOKEN}" ]]; then
  echo "ERROR: failed to create token for ${SPOKE_READER_NAMESPACE}/${SPOKE_READER_SERVICE_ACCOUNT}" >&2
  exit 1
fi

echo "--- Reading spoke cluster connection data"
SERVER="$(kubectl config view --raw -o jsonpath="{.clusters[?(@.name==\"kind-${SPOKE_CLUSTER}\")].cluster.server}")"
CADATA="$(kubectl config view --raw -o jsonpath="{.clusters[?(@.name==\"kind-${SPOKE_CLUSTER}\")].cluster.certificate-authority-data}")"
if [[ -z "${SERVER}" || -z "${CADATA}" ]]; then
  echo "ERROR: failed to resolve spoke server or CA data from kubeconfig" >&2
  exit 1
fi

create_kind_cluster "${HUB_CLUSTER}"
wait_for_cluster "kind-${HUB_CLUSTER}"
kind get kubeconfig --name "${HUB_CLUSTER}" > "${TMP_DIR}/hub.kubeconfig"

echo "--- Installing ClusterProfile CRD on hub"
go -C "${REPO_ROOT}/examples/cluster-inventory-api" mod download sigs.k8s.io/cluster-inventory-api
CLUSTER_INVENTORY_API_DIR="$(go -C "${REPO_ROOT}/examples/cluster-inventory-api" list -m -f '{{.Dir}}' sigs.k8s.io/cluster-inventory-api)"
CLUSTER_PROFILE_CRD="${CLUSTER_INVENTORY_API_DIR}/config/crd/bases/multicluster.x-k8s.io_clusterprofiles.yaml"
if [[ ! -f "${CLUSTER_PROFILE_CRD}" ]]; then
  echo "ERROR: ClusterProfile CRD not found at ${CLUSTER_PROFILE_CRD}" >&2
  exit 1
fi
kubectl --context "kind-${HUB_CLUSTER}" apply -f "${CLUSTER_PROFILE_CRD}"
kubectl --context "kind-${HUB_CLUSTER}" wait --for=condition=Established crd/clusterprofiles.multicluster.x-k8s.io --timeout=60s

echo "--- Creating hub namespaces and kubeconfig Secret"
kubectl --context "kind-${HUB_CLUSTER}" create namespace "${CONSUMER_NAMESPACE}" --dry-run=client -o yaml | kubectl --context "kind-${HUB_CLUSTER}" apply -f -
kubectl --context "kind-${HUB_CLUSTER}" create namespace "${PROFILE_NAMESPACE}" --dry-run=client -o yaml | kubectl --context "kind-${HUB_CLUSTER}" apply -f -
SPOKE_KUBECONFIG="${TMP_DIR}/spoke.kubeconfig"
cat > "${SPOKE_KUBECONFIG}" <<EOF
apiVersion: v1
kind: Config
clusters:
  - name: ${PROFILE_NAME}
    cluster:
      server: ${SERVER}
      certificate-authority-data: ${CADATA}
users:
  - name: ${SPOKE_READER_SERVICE_ACCOUNT}
    user:
      token: ${TOKEN}
contexts:
  - name: ${PROFILE_NAME}
    context:
      cluster: ${PROFILE_NAME}
      user: ${SPOKE_READER_SERVICE_ACCOUNT}
current-context: ${PROFILE_NAME}
EOF
kubectl --context "kind-${HUB_CLUSTER}" create secret generic "${PROFILE_NAME}" \
  --namespace "${CONSUMER_NAMESPACE}" \
  --from-file=Config="${SPOKE_KUBECONFIG}" \
  --dry-run=client -o yaml | kubectl --context "kind-${HUB_CLUSTER}" apply -f -

echo "--- Creating ClusterProfile"
kubectl --context "kind-${HUB_CLUSTER}" apply -f - <<EOF
apiVersion: multicluster.x-k8s.io/v1alpha1
kind: ClusterProfile
metadata:
  name: ${PROFILE_NAME}
  namespace: ${PROFILE_NAMESPACE}
spec:
  clusterManager:
    name: demo
EOF

NOW="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
STATUS_PATCH="$(cat <<EOF
{
  "status": {
    "accessProviders": [
      {
        "name": "kubeconfig-secretreader",
        "cluster": {
          "server": "${SERVER}",
          "certificate-authority-data": "${CADATA}",
          "extensions": [
            {
              "name": "client.authentication.k8s.io/exec",
              "extension": {
                "name": "${PROFILE_NAME}",
                "key": "Config",
                "namespace": "${CONSUMER_NAMESPACE}",
                "context": "${PROFILE_NAME}"
              }
            }
          ]
        }
      }
    ],
    "conditions": [
      {
        "type": "ControlPlaneHealthy",
        "status": "True",
        "reason": "E2E",
        "message": "spoke control plane is reachable for e2e",
        "lastTransitionTime": "${NOW}"
      }
    ]
  }
}
EOF
)"

kubectl --context "kind-${HUB_CLUSTER}" patch clusterprofile "${PROFILE_NAME}" \
  --namespace "${PROFILE_NAMESPACE}" \
  --type=merge \
  --subresource=status \
  -p "${STATUS_PATCH}"

echo "--- Demo setup completed"
