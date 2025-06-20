# Kubeconfig Provider Example

This example demonstrates how to use the kubeconfig provider to manage multiple Kubernetes clusters using kubeconfig secrets.

## Overview

The kubeconfig provider allows you to:
1. Discover and connect to multiple Kubernetes clusters using kubeconfig secrets
2. Run controllers that can operate across all discovered clusters
3. Manage cluster access through RBAC rules and service accounts

## Directory Structure

```
examples/kubeconfig/
├── scripts/                 # Utility scripts
│   ├── create-kubeconfig-secret.sh
│   └── rules.yaml          # Default RBAC rules template
└── main.go                 # Example operator implementation
```

## Usage

### 1. Setting Up Cluster Access

Before creating a kubeconfig secret, ensure that:
1. The remote cluster has a service account with the necessary RBAC permissions for your operator
2. The service account exists in the namespace where you want to create the kubeconfig secret

Use the `create-kubeconfig-secret.sh` script to create a kubeconfig secret for each cluster you want to manage:

```bash
./scripts/create-kubeconfig-secret.sh \
  --name cluster1 \
  -n default \
  -c prod-cluster \
  -a my-service-account
```

The script will:
- Use the specified service account from the remote cluster
- Generate a kubeconfig using the service account's token
- Store the kubeconfig in a secret in your local cluster
- Automatically create RBAC resources (Role/ClusterRole and bindings) with permissions defined in `rules.yaml` 

#### Command Line Options

- `-c, --context`: Kubeconfig context to use (required)
- `--name`: Name for the secret (defaults to context name)
- `-n, --namespace`: Namespace to create the secret in (default: "default")
- `-a, --service-account`: Service account name to use from the remote cluster (default: "multicluster-kubeconfig-provider")
- `-t, --role-type`: Create Role or ClusterRole (`role`|`clusterrole`) (default: "clusterrole")
- `-r, --rules-file`: Path to custom rules file (default: `rules.yaml` in script directory)
- `--skip-create-rbac`: Skip creating RBAC resources (Role/ClusterRole and bindings)
- `-h, --help`: Show help message

#### Examples

```bash
# Basic usage with default settings
./scripts/create-kubeconfig-secret.sh -c prod-cluster

# Create namespace-scoped Role instead of ClusterRole
./scripts/create-kubeconfig-secret.sh -c prod-cluster -t role

# Use custom RBAC rules file
./scripts/create-kubeconfig-secret.sh -c prod-cluster -r ./custom-rules.yaml

# Skip RBAC creation (manual RBAC setup)
./scripts/create-kubeconfig-secret.sh -c prod-cluster --skip-create-rbac

# Full example with all options
./scripts/create-kubeconfig-secret.sh \
  --name my-cluster \
  -n my-namespace \
  -c prod-cluster \
  -a my-service-account \
  -t clusterrole \
  -r ./my-rules.yaml
```

### 2. RBAC Configuration

The script automatically creates RBAC resources with the necessary permissions for your operator. By default, it uses the rules defined in `scripts/rules.yaml`:

```yaml
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["list", "get", "watch"]
```

#### Customizing RBAC Rules

You can customize the RBAC permissions by:

1. **Editing the default rules file** (`scripts/rules.yaml`):
```yaml
rules:
  - apiGroups: [""]
    resources: ["configmaps", "secrets", "pods"]
    verbs: ["list", "get", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["list", "get", "watch"]
```

2. **Using a custom rules file** with the `-r` option:
```bash
./scripts/create-kubeconfig-secret.sh -c prod-cluster -r ./my-custom-rules.yaml
```

3. **Choosing between Role and ClusterRole**:
   - Use `-t role` for namespace-scoped permissions
   - Use `-t clusterrole` (default) for cluster-wide permissions

#### RBAC Resource Creation

The script creates the following RBAC resources automatically:

- **Service Account**: If it doesn't exist, creates the specified service account
- **Role/ClusterRole**: With the permissions defined in the rules file
- **RoleBinding/ClusterRoleBinding**: Binds the service account to the role

#### Skipping RBAC Creation

If you prefer to manage RBAC manually, use the `--skip-create-rbac` flag:

```bash
./scripts/create-kubeconfig-secret.sh -c prod-cluster --skip-create-rbac
```

This will only create the kubeconfig secret without setting up any RBAC resources.

### 3. Implementing Your Operator

Add your controllers to `main.go`:

```go
func main() {
  err = mcbuilder.ControllerManagedBy(mgr).
		Named("multicluster-configmaps").
		For(&corev1.ConfigMap{}). // object to watch
		Complete(mcreconcile.Func(
			func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
				// reconcile logic

				return ctrl.Result{}, nil
			},
		))
}
```

Your controllers can then use the manager to access any cluster and view the resources that the RBAC permissions allow.

## How It Works

1. The kubeconfig provider watches for secrets with a specific label in a namespace
2. When a new secret is found, it:
   - Extracts the kubeconfig data
   - Creates a new controller-runtime cluster
   - Makes the cluster available to your controllers
3. Your controllers can access any cluster through the manager
4. RBAC Rules on the remote clusters ensure the SA operator on the cluster of the controller has the necessary permissions in the remote clusters

## Labels and Configuration

The provider uses the following labels and keys by default:
- Label: `sigs.k8s.io/multicluster-runtime-kubeconfig: "true"`
- Secret data key: `kubeconfig`

You can customize these in the provider options when creating it.

## Prerequisites

- `kubectl` configured with access to both the local and remote clusters
- `yq` command-line tool installed (required for RBAC rule processing)
- Service account with appropriate permissions in the remote cluster (if not using automatic RBAC creation script) 