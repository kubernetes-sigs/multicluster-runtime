#!/bin/bash

# Script to create a kubeconfig secret for the pod lister controller

set -e

# Default values
NAMESPACE="default"
SERVICE_ACCOUNT="multicluster-kubeconfig-provider"
KUBECONFIG_CONTEXT=""
SECRET_NAME=""
ROLE_TYPE="clusterrole"
RULES_FILE=""
CREATE_RBAC="true"

# Check for yq
if ! command -v yq &>/dev/null; then
  echo "ERROR: 'yq' is required but not installed. Please install yq (https://mikefarah.gitbook.io/yq/) and try again."
  exit 1
fi

# Function to display usage information
function show_help {
  echo "Usage: $0 [options]"
  echo "  -c, --context CONTEXT    Kubeconfig context to use (required)"
  echo "  --name NAME              Name for the secret (defaults to context name)"
  echo "  -n, --namespace NS       Namespace to create the secret in (default: ${NAMESPACE})"
  echo "  -a, --service-account SA Service account name to use (default: ${SERVICE_ACCOUNT})"
  echo "  -t, --role-type TYPE     Create Role or ClusterRole (role|clusterrole) (default: clusterrole)"
  echo "  -r, --rules-file FILE    Path to rules file (default: rules.yaml in script directory)"
  echo "  --skip-create-rbac       Skip creating RBAC resources (Role/ClusterRole and bindings)"
  echo "  -h, --help               Show this help message"
  echo ""
  echo "Examples:"
  echo "  $0 -c prod-cluster"
  echo "  $0 -c prod-cluster -t role -r ./custom-rules.yaml"
  echo "  $0 -c prod-cluster -t clusterrole"
  echo "  $0 -c prod-cluster --skip-create-rbac"
}

# Function to create Role or ClusterRole
function create_rbac {
  local role_type="$1"
  local rules_file="$2"
  local role_name="$3"
  local namespace="$4"
  
  if [ ! -f "$rules_file" ]; then
    echo "ERROR: Rules file not found: $rules_file"
    exit 1
  fi
  
  echo "Creating ${role_type} '${role_name}'..."
  
  if [ "$role_type" = "role" ]; then
    # Create Role
    ROLE_YAML=$(cat <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ${role_name}
  namespace: ${namespace}
rules:
$(yq '.rules' "$rules_file")
EOF
)
    
    echo "$ROLE_YAML" | kubectl --context=${KUBECONFIG_CONTEXT} apply -f -
    
    # Create RoleBinding
    ROLEBINDING_YAML=$(cat <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${role_name}-binding
  namespace: ${namespace}
subjects:
- kind: ServiceAccount
  name: ${SERVICE_ACCOUNT}
  namespace: ${namespace}
roleRef:
  kind: Role
  name: ${role_name}
  apiGroup: rbac.authorization.k8s.io
EOF
)
    
    echo "$ROLEBINDING_YAML" | kubectl --context=${KUBECONFIG_CONTEXT} apply -f -
    
  else
    # Create ClusterRole
    CLUSTERROLE_YAML=$(cat <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${role_name}
rules:
$(yq '.rules' "$rules_file")
EOF
)
    
    echo "$CLUSTERROLE_YAML" | kubectl --context=${KUBECONFIG_CONTEXT} apply -f -
    
    # Create ClusterRoleBinding
    CLUSTERROLEBINDING_YAML=$(cat <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${role_name}-binding
subjects:
- kind: ServiceAccount
  name: ${SERVICE_ACCOUNT}
  namespace: ${namespace}
roleRef:
  kind: ClusterRole
  name: ${role_name}
  apiGroup: rbac.authorization.k8s.io
EOF
)
    
    echo "$CLUSTERROLEBINDING_YAML" | kubectl --context=${KUBECONFIG_CONTEXT} apply -f -
  fi
  
  echo "$(tr '[:lower:]' '[:upper:]' <<< ${role_type:0:1})${role_type:1} '${role_name}' created successfully!"
}

# Function to create service account if it doesn't exist
function ensure_service_account {
  local namespace="$1"
  local service_account="$2"
  
  echo "Checking if service account '${service_account}' exists in namespace '${namespace}'..."
  
  # Check if service account exists
  if ! kubectl --context=${KUBECONFIG_CONTEXT} get serviceaccount ${service_account} -n ${namespace} &>/dev/null; then
    echo "Service account '${service_account}' not found in namespace '${namespace}'. Creating..."
    
    # Create the service account
    SERVICE_ACCOUNT_YAML=$(cat <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${service_account}
  namespace: ${namespace}
EOF
)
    
    echo "$SERVICE_ACCOUNT_YAML" | kubectl --context=${KUBECONFIG_CONTEXT} apply -f -
    echo "Service account '${service_account}' created successfully in namespace '${namespace}'"
  else
    echo "Service account '${service_account}' already exists in namespace '${namespace}'"
  fi
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
    -t|--role-type)
      ROLE_TYPE="$2"
      shift 2
      ;;
    -r|--rules-file)
      RULES_FILE="$2"
      shift 2
      ;;
    --skip-create-rbac)
      CREATE_RBAC="false"
      shift
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

# Validate role type if specified
if [ -n "$ROLE_TYPE" ] && [ "$ROLE_TYPE" != "role" ] && [ "$ROLE_TYPE" != "clusterrole" ]; then
  echo "ERROR: Invalid role type '$ROLE_TYPE'. Must be 'role' or 'clusterrole'"
  show_help
  exit 1
fi

# Set default rules file if not specified
if [ -z "$RULES_FILE" ]; then
  RULES_FILE="$(dirname "$0")/rules.yaml"
fi

# Set secret name to context if not specified
if [ -z "$SECRET_NAME" ]; then
  SECRET_NAME="$KUBECONFIG_CONTEXT"
fi

# Create RBAC resources by default (unless --no-create-role is specified)
if [ "$CREATE_RBAC" = "true" ]; then
  create_rbac "$ROLE_TYPE" "$RULES_FILE" "$SECRET_NAME" "$NAMESPACE"
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

# Ensure service account exists
ensure_service_account "$NAMESPACE" "$SERVICE_ACCOUNT"

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
if ! kubectl --kubeconfig="$TEMP_KUBECONFIG" version &>/dev/null; then
  rm "$TEMP_KUBECONFIG"
  echo "ERROR: Failed to verify kubeconfig - unable to connect to cluster."
  echo "- Ensure that the service account '${NAMESPACE}/${SERVICE_ACCOUNT}' on cluster '${KUBECONFIG_CONTEXT}' exists and is properly configured."
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