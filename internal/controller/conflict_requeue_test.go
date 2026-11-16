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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

// A status update that loses an optimistic-concurrency race is not an error:
// the reconciler must come back shortly on a fixed delay instead of surfacing
// the conflict, and must not rely on the deprecated ctrl.Result.Requeue flag.
func TestNixBuilderReconcile_StatusConflictRequeuesAfterDelay(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		niov1alpha1.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
	}

	builder := &niov1alpha1.NixBuilder{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "infra"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(builder).
		WithStatusSubresource(builder).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(_ context.Context, _ client.Client, _ string,
				obj client.Object, _ ...client.SubResourceUpdateOption) error {
				return apierrors.NewConflict(
					schema.GroupResource{Group: niov1alpha1.GroupVersion.Group, Resource: "nixbuilders"},
					obj.GetName(), nil)
			},
		}).
		Build()
	r := &NixBuilderReconciler{Client: c, Scheme: scheme}

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(builder),
	})
	if err != nil {
		t.Fatalf("Reconcile returned error on status conflict: %v", err)
	}
	if res.RequeueAfter != conflictRequeue {
		t.Errorf("RequeueAfter = %v, want %v", res.RequeueAfter, conflictRequeue)
	}
}
