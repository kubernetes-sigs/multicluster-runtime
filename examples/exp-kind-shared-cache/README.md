# Experimental Shared Cache Example

Demonstrates the wildcard cache pattern for memory-efficient multi-cluster controllers.

This example uses experimental packages. This should not be used in production.

## Key Concepts

- **WildcardCache**: Aggregates watches from multiple clusters into one shared informer per GVK
- **ScopedCache**: Filtered view for a single cluster
- **WildcardCacheProvider**: Interface to access all clusters' data from a reconciler

## Usage

```bash
# Create test clusters
kind create cluster --name cluster-1
kind create cluster --name cluster-2

# Run the example
go run .
```

## Access Patterns

**Scoped** (single cluster):
```go
cl, _ := mgr.GetCluster(ctx, req.ClusterName)
cl.GetCache().List(ctx, list)  // Only this cluster
```

**Wildcard** (all clusters):
```go
if wcp, ok := mgr.GetProvider().(expcache.WildcardCacheProvider); ok {
    wcp.GetWildcardCache().List(ctx, list)  // All clusters
    
    for _, item := range list.Items {
        cluster := expcache.GetClusterName(&item)
    }
}
```
