# Cluster Inventory API Example

This example runs a multicluster-runtime controller that discovers a member cluster from a `ClusterProfile`, then watches `ConfigMap` objects in that member cluster.

The example uses the Cluster Inventory API `kubeconfig-secretreader` exec plugin through `AccessProvider`. This covers the same kubeconfig-in-Secret use case as the deprecated in-process Secret strategy, while keeping credential retrieval in the Cluster Inventory API access plugin.

## Prerequisites

- `docker`, `kind`, `kubectl`, and `go`
- A Kubernetes version that supports image volumes
- Run commands from the repository root

## Run the e2e example

Set up hub and spoke kind clusters:

```bash
bash ./examples/cluster-inventory-api/setup-kind-demo.sh
```

The setup script creates the spoke reader token with a `24h` lifetime by default. Override it with `SPOKE_READER_TOKEN_DURATION` when you need a different duration.

Run the controller in the hub cluster and verify that it reconciles a spoke `ConfigMap`:

```bash
bash ./examples/cluster-inventory-api/e2e-incluster.sh
```

Clean up the kind clusters:

```bash
bash ./examples/cluster-inventory-api/down.sh
```

You can also run the setup and e2e check through Make:

```bash
make test-e2e-cluster-inventory-api
```
