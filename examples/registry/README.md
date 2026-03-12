# Registry Example

The [registry](https://pkg.go.dev/sigs.k8s.io/multicluster-runtime@main/pkg/clusters#Registry) is a simple cluster cache.

It can be used as a cluster tracker by adding it to a manager as
a `Runnable` or as a standalone cluster store to manage sets of
clusters.

## Example

The example requires a kubeconfig pointing to a running cluster and
showcases adding the registry as a `Runnable` to a manager.

It uses the namespace provider to yield namespaces in the running
cluster as clusters to the manager. Every second the clusters known to
the registry will be printed to stdout:

```text
current clusters:
[...]
current clusters:
  - default
  - kube-node-lease
  - kube-public
  - kube-system
  - local-path-storage
```

Create a namespace in the cluster:

```text
> kubectl create namespace test
```

And it will show up in the output when the provider has engaged the manager with the cluster:

```text
current clusters:
  [...]
I0313 00:24:09.447817    5156 provider.go:78] "Encountered namespace" logger="namespaced-cluster-provider" namespace="test"
current clusters:
  - default
  - kube-node-lease
  - kube-public
  - kube-system
  - local-path-storage
  - test
```
