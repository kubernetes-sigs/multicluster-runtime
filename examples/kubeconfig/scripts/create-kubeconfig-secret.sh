#!/bin/bash

# Script to create a kubeconfig secret for the pod lister controller

set -e

# Default values
NAMESPACE="default"
SERVICE_ACCOUNT="default"
KUBECONFIG_CONTEXT=""
SECRET_NAME=""

# Function to display usage information
function show_help {
  echo "Usage: $0 [options]"
  echo "  -c, --context CONTEXT    Kubeconfig context to use (required)"
  echo "  --name NAME              Name for the secret (defaults to context name)"
  echo "  -n, --namespace NS       Namespace to create the secret in (default: ${NAMESPACE})"
  echo "  -a, --service-account SA Service account name to use (default: ${SERVICE_ACCOUNT})"
  echo "  -h, --help               Show this help message"
  echo ""
  echo "Example: $0 -c prod-cluster"
}

# Parse command line options
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
    --name)
      SECRET_NAME="$2"
      shift 2
      ;;
    -n|--namespace)
      NAMESPACE="$2"
      shift 2
      ;;
    -c|--context)
      KUBECONFIG_CONTEXT="$2"
      shift 2
      ;;
    -a|--service-account)
      SERVICE_ACCOUNT="$2"
      shift 2
      ;;
    -h|--help)
      show_help
      exit 0
      ;;
    *)
      echo "Unknown option: $1"
      show_help
      exit 1
      ;;
  esac
done

# Validate required arguments
if [ -z "$KUBECONFIG_CONTEXT" ]; then
  echo "ERROR: Kubeconfig context is required (-c, --context)"
  show_help
  exit 1
fi

# Set secret name to context if not specified
if [ -z "$SECRET_NAME" ]; then
  SECRET_NAME="$KUBECONFIG_CONTEXT"
fi

# Get the cluster CA certificate from the remote cluster
CLUSTER_CA=$(kubectl --context=${KUBECONFIG_CONTEXT} config view --raw --minify --flatten -o jsonpath='{.clusters[].cluster.certificate-authority-data}')
if [ -z "$CLUSTER_CA" ]; then
  echo "ERROR: Could not get cluster CA certificate"
  exit 1
fi

# Get the cluster server URL from the remote cluster
CLUSTER_SERVER=$(kubectl --context=${KUBECONFIG_CONTEXT} config view --raw --minify --flatten -o jsonpath='{.clusters[].cluster.server}')
if [ -z "$CLUSTER_SERVER" ]; then
  echo "ERROR: Could not get cluster server URL"
  exit 1
fi

# Get the service account token from the remote cluster
SA_TOKEN=$(kubectl --context=${KUBECONFIG_CONTEXT} -n ${NAMESPACE} create token ${SERVICE_ACCOUNT} --duration=8760h)
if [ -z "$SA_TOKEN" ]; then
  echo "ERROR: Could not create service account token"
  exit 1
fi

# Create a new kubeconfig using the service account token
NEW_KUBECONFIG=$(cat <<EOF
apiVersion: v1
kind: Config
clusters:
- name: ${SECRET_NAME}
  cluster:
    server: ${CLUSTER_SERVER}
    certificate-authority-data: ${CLUSTER_CA}
contexts:
- name: ${SECRET_NAME}
  context:
    cluster: ${SECRET_NAME}
    user: ${SERVICE_ACCOUNT}
current-context: ${SECRET_NAME}
users:
- name: ${SERVICE_ACCOUNT}
  user:
    token: ${SA_TOKEN}
EOF
)

# Save kubeconfig temporarily for testing
TEMP_KUBECONFIG=$(mktemp)
echo "$NEW_KUBECONFIG" > "$TEMP_KUBECONFIG"

# Verify the kubeconfig works
echo "Verifying kubeconfig..."
if ! kubectl --kubeconfig="$TEMP_KUBECONFIG" get pods -A &>/dev/null; then
  rm "$TEMP_KUBECONFIG"
  echo "ERROR: Failed to verify kubeconfig - unable to list pods."
  echo "- Ensure that the service account '${NAMESPACE}/${SERVICE_ACCOUNT}' on cluster '${KUBECONFIG_CONTEXT}' has the necessary permissions to list pods."
  echo "- You may specify a namespace using the -n flag."
  echo "- You may specify a service account using the -a flag."
  exit 1
fi
echo "Kubeconfig verified successfully!"

# Encode the verified kubeconfig
KUBECONFIG_B64=$(cat "$TEMP_KUBECONFIG" | base64 -w0)
rm "$TEMP_KUBECONFIG"

# Generate and apply the secret
SECRET_YAML=$(cat <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${SECRET_NAME}
  namespace: ${NAMESPACE}
  labels:
    sigs.k8s.io/multicluster-runtime-kubeconfig: "true"
type: Opaque
data:
  kubeconfig: ${KUBECONFIG_B64}
EOF
)

echo "Creating kubeconfig secret..."
echo "$SECRET_YAML" | kubectl apply -f -

echo "Secret '${SECRET_NAME}' created in namespace '${NAMESPACE}'"
echo "The operator should now be able to discover and connect to this cluster" 