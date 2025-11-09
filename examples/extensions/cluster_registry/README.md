# Extension: Cluster Registry Example

This example demonstrates how to use the Cluster Registry extension together with a cluster provider (Kind in this case) to build a simple multi-cluster controller. The example watches ConfigMaps in any connected cluster and copies them to all the other clusters if they don't already exist.

## Overview

The Cluster Registry extension keeps track of the set of connected cluster names inside the multicluster manager so that your controllers can easily iterate over all clusters.

In this example:
- The Kind provider discovers all local Kind clusters
- The Cluster Registry extension maintains their names
- A controller watches ConfigMaps in every cluster and, when a ConfigMap is created in one cluster, copies it to the other clusters if it doesn't already exist there

Notes/limitations of the demo controller:
- It only copies newly found ConfigMaps (no updates are propagated)
- It does not delete ConfigMaps from other clusters when the source is deleted
- It assumes the target namespaces already exist in the other clusters

## Prerequisites

- Docker running on your machine
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/) installed and available in your PATH
- `kubectl` configured to access your Kind clusters
- Go 1.22+ (to run the example)

## Create two Kind clusters

```bash
kind create cluster --name fleet-alpha
kind create cluster --name fleet-beta
```

This will create two clusters with contexts:
- kind-fleet-alpha
- kind-fleet-beta

## Run the example

From this directory:

```bash
go run ./main.go
```

You should see logs indicating clusters were discovered and a controller started.

## Try it out

Open two terminals and watch events on both clusters:

```bash
# Terminal 1
kubectl get events -A --context kind-fleet-alpha --watch

# Terminal 2
kubectl get events -A --context kind-fleet-beta --watch
```

Create a ConfigMap in one cluster (for example, fleet-alpha):

```bash
kubectl --context kind-fleet-alpha -n default create configmap demo --from-literal=hello=world
```

Observe that the controller logs a reconcile for cluster kind-fleet-alpha and then creates the same ConfigMap in kind-fleet-beta if it doesn't exist yet. You should also see a Normal event recorded for the source ConfigMap.

You can verify the copy with:

```bash
kubectl --context kind-fleet-beta -n default get configmap demo -o yaml
```

## How it works (high level)

- The example uses the Kind provider to discover Kind clusters on your machine
- The Cluster Registry extension registers itself with the manager and exposes the list of cluster names
- The controller:
  1. Reconciles ConfigMaps in the cluster where the change happened (the "source")
  2. Iterates through cluster names from the cluster registry
  3. For each other cluster (the "targets"), creates the ConfigMap if it doesn't already exist

See main.go for the exact implementation.

## Cleanup

Delete the Kind clusters when done:

```bash
kind delete cluster --name fleet-alpha
kind delete cluster --name fleet-beta
```

