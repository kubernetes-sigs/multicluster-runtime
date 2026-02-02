# Exp(erimental) packages for multicluster-runtime

These set of packages are experimental to demonstrate concepts that may eventually be promoted to the main multicluster-runtime repository if they prove useful and they are upstreamable.

## Shared Informers for Multi-Cluster

This package provides shared informer implementations that can be used to watch and cache Kubernetes resources across multiple clusters. It includes support for scoped informers that can filter events based on specific cluster.

This is in a way fork of the standard Kubernetes client-go informers, with modifications to support multi-cluster scenarios by adding cluster aware key functions and reflectors.

This is still a work in progress and may not be fully functional or stable. Feedback and contributions are welcome!

This implementation has a few hacks, like lack of proper RESTMapper usage, which will be addressed in future iterations as the design matures.