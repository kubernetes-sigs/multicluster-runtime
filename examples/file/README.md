# file

The file provider reads kubeconfig files from the local filesystem.

It provides clusters named after the filepath and context name.

This example lists configmaps in the clusters discovered by the file
provider.

## Example

With a single kind cluster (`kind-kind` defined in `~/.kube/config`:

```bash
$ go run . -kubeconfig ~/.kube/config
2025-08-13T11:28:01+02:00       INFO    file-cluster-provider   file cluster provider initialized       {"kubeconfigFiles": ["/home/user/.kube/config"], "kubeconfigDirs": [], "kubeconfigGlobs": [""]}
2025-08-13T11:28:01+02:00       INFO    controller-runtime.metrics      Starting metrics server
2025-08-13T11:28:01+02:00       INFO    Starting Controller     {"controller": "multicluster-configmaps", "controllerGroup": "", "controllerKind": "ConfigMap"}
2025-08-13T11:28:01+02:00       INFO    Starting workers        {"controller": "multicluster-configmaps", "controllerGroup": "", "controllerKind": "ConfigMap", "worker count": 1}
2025-08-13T11:28:01+02:00       INFO    controller-runtime.metrics      Serving metrics server  {"bindAddress": ":8080", "secure": false}
2025-08-13T11:28:01+02:00       INFO    file-cluster-provider   adding cluster  {"name": "/home/user/.kube/config+kind-kind"}
2025-08-13T11:28:01+02:00       INFO    Starting EventSource    {"controller": "multicluster-configmaps", "controllerGroup": "", "controllerKind": "ConfigMap", "source": "func source: 0x172ca20"}
2025-08-13T11:28:01+02:00       INFO    Reconciling ConfigMap   {"controller": "multicluster-configmaps", "controllerGroup": "", "controllerKind": "ConfigMap", "reconcileID": "7096cf7e-f382-4f22-8d1b-bff768f99c07", "cluster": "/home/user/.kube/config+kind-kind"}
2025-08-13T11:28:01+02:00       INFO    ConfigMap found {"controller": "multicluster-configmaps", "controllerGroup": "", "controllerKind": "ConfigMap", "reconcileID": "7096cf7e-f382-4f22-8d1b-bff768f99c07", "cluster": "/home/user/.kube/config+kind-kind", "namespace": "default", "name": "kube-root-ca.crt", "cluster": "/home/user/.kube/config+kind-kind"}
...
```

Then adding another kind cluster with `kind create cluster --name test`:

```bash
2025-08-13T11:28:54+02:00       INFO    file-cluster-provider   received fsnotify event {"event": "\"/home/user/.kube/config.lock\": CREATE"}
2025-08-13T11:28:54+02:00       INFO    file-cluster-provider   received fsnotify event {"event": "\"/home/user/.kube/config\": WRITE"}
2025-08-13T11:28:54+02:00       INFO    file-cluster-provider   adding cluster  {"name": "/home/user/.kube/config+kind-test"}
2025-08-13T11:28:54+02:00       INFO    Starting EventSource    {"controller": "multicluster-configmaps", "controllerGroup": "", "controllerKind": "ConfigMap", "source": "func source: 0x172ca20"}
2025-08-13T11:28:54+02:00       INFO    file-cluster-provider   received fsnotify event {"event": "\"/home/user/.kube/config\": WRITE"}
2025-08-13T11:28:54+02:00       INFO    file-cluster-provider   received fsnotify event {"event": "\"/home/user/.kube/config.lock\": REMOVE"}
2025-08-13T11:28:54+02:00       INFO    Reconciling ConfigMap   {"controller": "multicluster-configmaps", "controllerGroup": "", "controllerKind": "ConfigMap", "reconcileID": "9d3e1e4b-b587-49b1-9fcf-a903f4e713f5", "cluster": "/home/user/.kube/config+kind-test"}
2025-08-13T11:28:54+02:00       INFO    ConfigMap found {"controller": "multicluster-configmaps", "controllerGroup": "", "controllerKind": "ConfigMap", "reconcileID": "9d3e1e4b-b587-49b1-9fcf-a903f4e713f5", "cluster": "/home/user/.kube/config+kind-test", "namespace": "local-path-storage", "name": "local-path-config", "cluster": "/home/user/.kube/config+kind-test"}
...
```

Adding new configmaps to either cluster will trigger a reconcile:

```bash
kubectl --context kind-test create configmap test --from-literal hello=word
```

```bash
2025-08-13T11:30:37+02:00       INFO    Reconciling ConfigMap   {"controller": "multicluster-configmaps", "controllerGroup": "", "controllerKind": "ConfigMap", "reconcileID": "56f49ce6-7ff8-4eb9-b3ac-ad7586265c6d", "cluster": "/home/user/.kube/config+kind-test"}
2025-08-13T11:30:37+02:00       INFO    ConfigMap found {"controller": "multicluster-configmaps", "controllerGroup": "", "controllerKind": "ConfigMap", "reconcileID": "56f49ce6-7ff8-4eb9-b3ac-ad7586265c6d", "cluster": "/home/user/.kube/config+kind-test", "namespace": "default", "name": "test", "cluster": "/home/user/.kube/config+kind-test"}
```
