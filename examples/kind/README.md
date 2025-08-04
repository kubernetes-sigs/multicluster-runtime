# Kind Provider Example

This example demonstrates how to use the Kind provider to manage multiple Kubernetes clusters created with Kind (Kubernetes in Docker).

## Overview

The Kind provider allows you to:
1. Automatically discover and connect to multiple Kind clusters
2. Run controllers that can operate across all discovered clusters
3. Test multicluster scenarios locally using Docker containers

## Prerequisites

- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/) installed and available in your PATH
- Docker running on your machine
- `kubectl` configured to access Kind clusters

## Usage

### 1. Create Kind Clusters

First, create multiple Kind clusters for testing:

```bash
kind create cluster --name fleet-alpha
kind create cluster --name fleet-beta
```

This will create two Kind clusters:
- `fleet-alpha` accessible via context `kind-fleet-alpha`
- `fleet-beta` accessible via context `kind-fleet-beta`

### 2. Run the Example

Start the multicluster operator:

```bash
go run ./main.go
```

The operator will automatically discover the Kind clusters and connect to them.

### 3. Observe Cross-Cluster Operations

In separate terminals, monitor events across both clusters to see the multicluster operations in action:

```bash
# Terminal 1: Monitor fleet-alpha cluster
kubectl get events -A --context kind-fleet-alpha --watch

# Terminal 2: Monitor fleet-beta cluster  
kubectl get events -A --context kind-fleet-beta --watch
```

## How It Works

1. The Kind provider automatically discovers Kind clusters by:
   - Scanning for Kind cluster using `"sigs.k8s.io/kind/pkg/cluster"`
   - Connecting to clusters that match the Kind naming pattern (`fleet-*`)
2. For each discovered cluster, it engages multicluster-runtime.
3. Your controllers can then operate across all discovered clusters

## Cleanup

To clean up the Kind clusters when you're done:

```bash
kind delete cluster --name fleet-alpha
kind delete cluster --name fleet-beta
```