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
├── controllers/                    # Example controller that simply lists pods
│   ├── pod_lister.go
├── scripts/                 # Utility scripts
│   └── create-kubeconfig-secret.sh
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

Command line options:
- `-c, --context`: Kubeconfig context to use (required)
- `--name`: Name for the secret (defaults to context name)
- `-n, --namespace`: Namespace to create the secret in (default: "default")
- `-a, --service-account`: Service account name to use from the remote cluster (default: "default")

### 2. Customizing RBAC Rules

The service account in the remote cluster must have the necessary RBAC permissions for your operator to function. Edit the RBAC templates in the `rbac/` directory to define the permissions your operator needs:

```yaml
# rbac/clusterrole.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${SECRET_NAME}-role
rules:
# Add permissions for your operator <--------------------------------
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["list", "get", "watch"]  # watch is needed for controllers that observe resources
```

Important RBAC considerations:
- Use `watch` verb if your controller needs to observe resource changes
- Use `list` and `get` for reading resources
- Use `create`, `update`, `patch`, `delete` for modifying resources
- Consider using `Role` instead of `ClusterRole` if you only need namespace-scoped permissions

### 3. Implementing Your Operator

Add your controllers to `main.go`:

```go
func main() {
    // Import your controllers here <--------------------------------
	"sigs.k8s.io/multicluster-runtime/examples/kubeconfig/controllers"

    //...

    // Run your controllers here <--------------------------------
	podWatcher := controllers.NewPodWatcher(mgr)
	if err := mgr.Add(podWatcher); err != nil {
		entryLog.Error(err, "Unable to add pod watcher")
		os.Exit(1)
	}
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
4. RBAC rules ensure your operator has the necessary permissions in each cluster

## Labels and Configuration

The provider uses the following labels and keys by default:
- Label: `sigs.k8s.io/multicluster-runtime-kubeconfig: "true"`
- Secret data key: `kubeconfig`

You can customize these in the provider options when creating it. 