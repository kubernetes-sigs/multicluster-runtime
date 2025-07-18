# file

The file provider reads kubeconfig files from the local filesystem.

It provides clusters named after the context name in the kubeconfig file.

This example reads kubeconfig files at the specified paths and lists the
collected clusters.

## Example

With a single kind cluster defined in `~/.kube/config`:

```bash
$ go run . -paths ~/.kube/config
Clusters:
- kind-kind
```
