/*
Copyright 2026.

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

package controller

import (
	"context"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

// Field indexes shared by all workload controllers so a Secret or Flux source
// change enqueues the workloads that reference it (design §8).
const (
	IndexByCredentialsSecret = "spec.nix.source.credentialsRef.name"
	IndexByFluxSource        = "spec.nix.source.fluxSourceRef.name"
)

// registerWorkloadIndexes registers the credentials-secret and flux-source name
// indexes for a workload kind. getSource extracts the NixSource from the object.
func registerWorkloadIndexes(mgr ctrl.Manager, proto client.Object, getSource func(client.Object) *niov1alpha1.NixSource) error {
	idx := mgr.GetFieldIndexer()
	if err := idx.IndexField(context.Background(), proto, IndexByCredentialsSecret, func(obj client.Object) []string {
		src := getSource(obj)
		if src == nil || src.CredentialsRef == nil {
			return nil
		}
		return []string{src.CredentialsRef.Name}
	}); err != nil {
		return err
	}
	return idx.IndexField(context.Background(), proto, IndexByFluxSource, func(obj client.Object) []string {
		src := getSource(obj)
		if src == nil || src.FluxSourceRef == nil {
			return nil
		}
		return []string{src.FluxSourceRef.Name}
	})
}

// enqueueByIndex returns the reconcile requests for every workload (of the kind
// backing listProto) whose index field equals the referenced object's name, in
// the referenced object's namespace.
func enqueueByIndex(c client.Client, listProto client.ObjectList, index string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		list := listProto.DeepCopyObject().(client.ObjectList)
		if err := c.List(ctx, list,
			client.InNamespace(obj.GetNamespace()),
			client.MatchingFields{index: obj.GetName()},
		); err != nil {
			return nil
		}
		items, err := apimeta.ExtractList(list)
		if err != nil {
			return nil
		}
		requests := make([]reconcile.Request, 0, len(items))
		for _, it := range items {
			m, err := apimeta.Accessor(it)
			if err != nil {
				continue
			}
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKey{Namespace: m.GetNamespace(), Name: m.GetName()},
			})
		}
		return requests
	}
}

// fluxSourceKinds are the Flux source kinds a workload may reference.
var fluxSourceKinds = []string{"GitRepository", "OCIRepository", "Bucket"}

// addFluxSourceWatches adds a Watches for each installed Flux source CRD, mapping
// a source change to the workloads that reference it. Kinds whose CRD is not
// installed are skipped so the manager starts cleanly without Flux (polling
// still picks up Flux-mode revisions).
func addFluxSourceWatches(b *builder.Builder, mgr ctrl.Manager, c client.Client, listProto client.ObjectList) {
	mapper := mgr.GetRESTMapper()
	mapFn := enqueueByIndex(c, listProto, IndexByFluxSource)
	for _, kind := range fluxSourceKinds {
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: fluxSourceGroup, Kind: kind})
		if err != nil {
			continue // CRD not installed; rely on polling
		}
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(mapping.GroupVersionKind)
		b.Watches(u, handler.EnqueueRequestsFromMapFunc(mapFn))
	}
}
