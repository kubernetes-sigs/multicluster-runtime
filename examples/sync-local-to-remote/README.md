# Syncing resources from local to provider clusters

This example demonstrates how to setup a controller using
multicluster-runtime that pushes ConfigMaps from a local cluster to all
provider clusters.

The same logic can be used to deploy resources based on a central
cluster to worker clusters, e.g. to implement a multi-cluster DaemonSet
controller.

## Example

Create some kind clusters:

```bash
kind create cluster --name cluster1 --kubeconfig ./cluster1.kubeconfig
kind create cluster --name cluster2 --kubeconfig ./cluster2.kubeconfig
kind create cluster --name cluster3 --kubeconfig ./cluster3.kubeconfig
```

The operator expects two flags: `-local-kubeconfig` and `-provider-kubeconfigs`.

`-local-kubeconfig` points to the kubeconfig of the local cluster where
the operator is running, would do leader election etc.pp. and from where
the configmaps are synchronized.

`-provider-kubeconfigs` is a comma-separated list of kubeconfig files of
clusters to which configmaps should be synchronized.

The kubeconfig passed to `-local-kubeconfig` can be the same as one of the
`-provider-kubeconfigs`. This does not matter for multicluster-runtime.

Run the operator:

```bash
go run . \
    -local-kubeconfig ./cluster1.kubeconfig \
    -provider-kubeconfigs ./cluster1.kubeconfig,./cluster2.kubeconfig,./cluster3.kubeconfig
```

Now create a configmap in the local cluster (cluster1):

```bash
$ kubectl --kubeconfig ./cluster1.kubeconfig apply -f example-cm.yaml
configmap/example-configmap created
```

And verify that the same configmap appears in the provider clusters:

```bash
$ kubectl --kubeconfig ./cluster2.kubeconfig get configmap example-configmap -o yaml
apiVersion: v1
data:
  hello: world
kind: ConfigMap
metadata:
  # ...
  labels:
    example.io/propagate: "true"
  name: example-configmap
  namespace: default
$ kubectl --kubeconfig ./cluster3.kubeconfig get configmap example-configmap -o yaml
apiVersion: v1
data:
  hello: world
kind: ConfigMap
metadata:
  # ...
  labels:
    example.io/propagate: "true"
  name: example-configmap
  namespace: default
```

When adding data in the local cluster the changes are propagated to the
provider clusters:

```bash
$ kubectl --kubeconfig ./cluster1.kubeconfig \
    patch configmap example-configmap \
    --type merge \
    -p '{"data": {"hello": "brian"}}'
configmap/example-configmap patched
```

Now the provider clusters are also greeting Dr. Kerningham:

```bash
$ kubectl --kubeconfig ./cluster2.kubeconfig get configmap example-configmap -o yaml
apiVersion: v1
data:
  hello: brian
kind: ConfigMap
metadata:
  # ...
  labels:
    example.io/propagate: "true"
  name: example-configmap
  namespace: default
$ kubectl --kubeconfig ./cluster3.kubeconfig get configmap example-configmap -o yaml
apiVersion: v1
data:
  hello: brian
kind: ConfigMap
metadata:
  # ...
  labels:
    example.io/propagate: "true"
  name: example-configmap
  namespace: default
```

And when modifying the configmaps in one of the provider clusters the
change is reverted back to the state of the local cluster:

```bash
$ kubectl --kubeconfig ./cluster2.kubeconfig \
    patch configmap example-configmap \
    --type merge \
    -p '{"data": {"hello": "hopper"}}'
configmap/example-configmap patched
```

Now the configmap in cluster2 is reverted back to "brian":

```bash
$ kubectl --kubeconfig ./cluster2.kubeconfig get configmap example-configmap -o yaml
apiVersion: v1
data:
  hello: brian
kind: ConfigMap
metadata:
  # ...
  labels:
    example.io/propagate: "true"
  name: example-configmap
  namespace: default
```
