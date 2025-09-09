[![Go Report Card](https://goreportcard.com/badge/sigs.k8s.io/multicluster-runtime)](https://goreportcard.com/report/sigs.k8s.io/multicluster-runtime)
[![godoc](https://pkg.go.dev/badge/sigs.k8s.io/multicluster-runtime)](https://pkg.go.dev/sigs.k8s.io/multicluster-runtime)

# multicluster-runtime

<img src="./contrib/logo/logo.png" width="300"/>

## Multi-Cluster Controllers with controller-runtime

multicluster-runtime is a Go library to write Kubernetes controllers that reconcile across a dynamic fleet of Kubernetes (or Kubernetes-like) clusters.

- **dynamic fleet orchestration**: So-called providers interact with multi-cluster solutions like Cluster API and dynamically start and stop reconciliation against clusters discovered through the provider.
- **no fork, no go mod replace**: clean extension to [upstream controller-runtime](https://github.com/kubernetes-sigs/controller-runtime).
- **universal**: Any kind of multi-cluster solution could theoretically be supported (kind, [cluster-api](https://github.com/kubernetes-sigs/cluster-api), [Gardener](https://gardener.cloud/), [kcp](https://kcp.io), BYO). Cluster providers make the controller-runtime multi-cluster aware.
- **seamless**: add multi-cluster support without compromising on single-cluster. Run in either mode without code changes to the reconcilers.

## Patterns Possible with multicluster-runtime

### Uniform Reconcilers

Run the same reconciler against many clusters:
- The reconciler reads from cluster A and writes to cluster A.
- The reconciler reads from cluster B and writes to cluster B.
- The reconciler reads from cluster C and writes to cluster C.

This is the most simple case. Many existing reconcilers can easily adapted to work like this without major code changes. The resulting controllers will work in the multi-cluster setting, but also in the classical single-cluster setup, all in the same code base.

![multi-cluster topologies uniform](https://github.com/user-attachments/assets/b91a3aac-6a1c-481e-8961-2f25605aeffe)

### Multi-Cluster-Aware Reconcilers

Run reconcilers that listen to some cluster(s) and operate other clusters.
![multi-cluster topologies multi](https://github.com/user-attachments/assets/d7e37c39-66e3-4912-89ac-5441f0ad5669)

## Principles

1. multicluster-runtime is a friendly extension of controller-runtime.
2. multicluster-runtime loves ❤️ contributions.
3. multicluster-runtime is following controller-runtime releases.
4. multicluster-runtime is developed as if it was part of controller-runtime (quality standards, naming, style).
5. multicluster-runtime could be a testbed for native controller-runtime functionality, eventually becoming superfluous.
6. multicluster-runtime is provider agnostic, but may contain providers with its own go.mod files and dedicated OWNERS files.
    * These providers should be understood as reference implementations and are generally limited to projects hosted by a Kubernetes SIG.
    * "Production-ready" providers shall be hosted in their own respective repositories close to the project they support.

## Provider Ecosystem

> [!NOTE]
> If you are developing a multicluster-runtime provider, please feel free to open a PR and add your implementation to this list!

The following cluster provider implementations exist outside of this repository:

- [kcp-dev/multicluster-provider](https://github.com/kcp-dev/multicluster-provider): Collection of multicluster-runtime providers to interact with [kcp](https://kcp.io)'s logical clusters.
- [gardener/multicluster-provider](https://github.com/gardener/multicluster-provider): A multicluster-runtime provider to interact with [Gardener](https://gardener.cloud)'s shoot clusters.

## Code Sample

```golang
package main

import (
	"context"
	"log"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/providers/kind"
)

func main() {
	ctx := signals.SetupSignalHandler()

	provider := kind.New()
	mgr, err := mcmanager.New(ctrl.GetConfigOrDie(), provider, manager.Options{})
	if err != nil {
		log.Fatal(err, "unable to create manager")
	}

	err = mcbuilder.ControllerManagedBy(mgr).
		Named("multicluster-configmaps").
		For(&corev1.ConfigMap{}).
		Complete(mcreconcile.Func(
			func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
				cl, err := mgr.GetCluster(ctx, req.ClusterName)
				if err != nil {
					return reconcile.Result{}, err
				}

				cm := &corev1.ConfigMap{}
				if err := cl.GetClient().Get(ctx, req.Request.NamespacedName, cm); err != nil {
					if apierrors.IsNotFound(err) {
						return reconcile.Result{}, nil
					}
					return reconcile.Result{}, err
				}

				log.Printf("ConfigMap %s/%s in cluster %q", cm.Namespace, cm.Name, req.ClusterName)

				return ctrl.Result{}, nil
			},
		))
	if err != nil {
		log.Fatal(err, "unable to create controller")
	}

	go provider.Run(ctx, mgr)
	if err := mgr.Start(ctx); err != nil {
		log.Fatal(err, "unable to run manager")
	}
}
```

## FAQs

### How is this different from https://github.com/admiraltyio/multicluster-controller ?

In contrast to https://github.com/admiraltyio/multicluster-controller, multicluster-runtime keeps building on controller-runtime for most of its constructs. It is not replacing the manager, the controller or the cluster. To a large degree, this became possible through the extensive use of generics in controller-runtime. Most multicluster-runtime constructs are just type instantiations with a little glue.

### Can I dynamically load provider plugins?

No, plugins are out of scope for now. Multicluster-runtime needs source code changes to 
1. enable multi-cluster support by replacing some controller-runtime imports with the multicluster-runtime equivalents and
2. wire supported providers.
The provider interface is simple. So it is not ruled out to have some plugin mechanism in the future.
