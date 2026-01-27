/*
Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package namespace

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ client.Client = &NamespacedClient{}

// NamespacedClient is a client that operates on a specific namespace.
type NamespacedClient struct {
	clusterName string
	client.Client
}

// Get returns a single object.
func (n *NamespacedClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if ns := key.Namespace; ns != metav1.NamespaceDefault {
		return apierrors.NewInvalid(obj.GetObjectKind().GroupVersionKind().GroupKind(), obj.GetName(),
			field.ErrorList{field.Invalid(field.NewPath("metadata", "namespace"), ns, "must be 'default'")})
	}
	key.Namespace = n.clusterName
	err := n.Client.Get(ctx, key, obj, opts...)
	if err != nil {
		return err
	}
	obj.SetNamespace(metav1.NamespaceDefault)
	return nil
}

// List returns a list of objects.
func (n *NamespacedClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	var copts client.ListOptions
	for _, o := range opts {
		o.ApplyToList(&copts)
	}
	if copts.Namespace != metav1.NamespaceDefault {
		return apierrors.NewNotFound(schema.GroupResource{}, copts.Namespace)
	}
	if err := n.Client.List(ctx, list, append(opts, client.InNamespace(n.clusterName))...); err != nil {
		return err
	}
	return meta.EachListItem(list, func(obj runtime.Object) error {
		obj.(client.Object).SetNamespace(metav1.NamespaceDefault)
		return nil
	})
}

// Create creates a new object.
func (n *NamespacedClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if ns := obj.GetNamespace(); ns != metav1.NamespaceDefault {
		return apierrors.NewInvalid(obj.GetObjectKind().GroupVersionKind().GroupKind(), obj.GetName(),
			field.ErrorList{field.Invalid(field.NewPath("metadata", "namespace"), ns, "must be 'default'")})
	}
	obj.SetNamespace(n.clusterName)
	defer obj.SetNamespace(metav1.NamespaceDefault)
	return n.Client.Create(ctx, obj, opts...)
}

// Delete deletes an object.
func (n *NamespacedClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if ns := obj.GetNamespace(); ns != metav1.NamespaceDefault {
		return apierrors.NewInvalid(obj.GetObjectKind().GroupVersionKind().GroupKind(), obj.GetName(),
			field.ErrorList{field.Invalid(field.NewPath("metadata", "namespace"), ns, "must be 'default'")})
	}
	obj.SetNamespace(n.clusterName)
	defer obj.SetNamespace(metav1.NamespaceDefault)
	return n.Client.Delete(ctx, obj, opts...)
}

// Update updates an object.
func (n *NamespacedClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if ns := obj.GetNamespace(); ns != metav1.NamespaceDefault {
		return apierrors.NewInvalid(obj.GetObjectKind().GroupVersionKind().GroupKind(), obj.GetName(),
			field.ErrorList{field.Invalid(field.NewPath("metadata", "namespace"), ns, "must be 'default'")})
	}
	obj.SetNamespace(n.clusterName)
	defer obj.SetNamespace(metav1.NamespaceDefault)
	return n.Client.Update(ctx, obj, opts...)
}

// Patch patches an object.
func (n *NamespacedClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	// TODO(sttts): this is not thas easy here. We likely have to support all the different patch types.
	//              But other than that, this is just an example provider, so ¯\_(ツ)_/¯.
	panic("implement the three patch types")
}

// Apply applies an apply configuration to an object.
func (n *NamespacedClient) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	return n.Client.Apply(ctx, obj, opts...)
}

// DeleteAllOf deletes all objects of the given type.
func (n *NamespacedClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	if ns := obj.GetNamespace(); ns != metav1.NamespaceDefault {
		return apierrors.NewInvalid(obj.GetObjectKind().GroupVersionKind().GroupKind(), obj.GetName(),
			field.ErrorList{field.Invalid(field.NewPath("metadata", "namespace"), ns, "must be 'default'")})
	}
	obj.SetNamespace(n.clusterName)
	defer obj.SetNamespace(metav1.NamespaceDefault)
	return n.Client.DeleteAllOf(ctx, obj, opts...)
}

// Status returns a subresource writer.
func (n *NamespacedClient) Status() client.SubResourceWriter {
	return &SubResourceNamespacedClient{clusterName: n.clusterName, client: n.Client.SubResource("status")}
}

// SubResource returns a subresource client.
func (n *NamespacedClient) SubResource(subResource string) client.SubResourceClient {
	return &SubResourceNamespacedClient{clusterName: n.clusterName, client: n.Client.SubResource(subResource)}
}

var _ client.SubResourceClient = &SubResourceNamespacedClient{}

// SubResourceNamespacedClient is a client that operates on a specific namespace
// and subresource.
type SubResourceNamespacedClient struct {
	clusterName string
	client      client.SubResourceClient
}

// Get returns a single object from a subresource.
func (s SubResourceNamespacedClient) Get(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceGetOption) error {
	if ns := obj.GetNamespace(); ns != metav1.NamespaceDefault {
		return apierrors.NewInvalid(obj.GetObjectKind().GroupVersionKind().GroupKind(), obj.GetName(),
			field.ErrorList{field.Invalid(field.NewPath("metadata", "namespace"), ns, "must be 'default'")})
	}
	obj.SetNamespace(s.clusterName)
	defer obj.SetNamespace(metav1.NamespaceDefault)
	defer subResource.SetNamespace(metav1.NamespaceDefault)
	return s.client.Get(ctx, obj, subResource, opts...)
}

// Create creates a new object in a subresource.
func (s SubResourceNamespacedClient) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	if ns := obj.GetNamespace(); ns != metav1.NamespaceDefault {
		return apierrors.NewInvalid(obj.GetObjectKind().GroupVersionKind().GroupKind(), obj.GetName(),
			field.ErrorList{field.Invalid(field.NewPath("metadata", "namespace"), ns, "must be 'default'")})
	}
	obj.SetNamespace(s.clusterName)
	defer obj.SetNamespace(metav1.NamespaceDefault)
	defer subResource.SetNamespace(metav1.NamespaceDefault)
	return s.client.Create(ctx, obj, subResource, opts...)
}

// Update updates an object in a subresource.
func (s SubResourceNamespacedClient) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if ns := obj.GetNamespace(); ns != metav1.NamespaceDefault {
		return apierrors.NewInvalid(obj.GetObjectKind().GroupVersionKind().GroupKind(), obj.GetName(),
			field.ErrorList{field.Invalid(field.NewPath("metadata", "namespace"), ns, "must be 'default'")})
	}
	obj.SetNamespace(s.clusterName)
	defer obj.SetNamespace(metav1.NamespaceDefault)
	return s.client.Update(ctx, obj, opts...)
}

// Patch patches an object in a subresource.
func (s SubResourceNamespacedClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	// TODO(sttts): this is not thas easy here. We likely have to support all the different patch types.
	//              But other than that, this is just an example provider, so ¯\_(ツ)_/¯.
	panic("implement the three patch types")
}

// Apply applies an apply configuration to a subresource.
func (s SubResourceNamespacedClient) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	return s.client.Apply(ctx, obj, opts...)
}
