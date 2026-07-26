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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

func TestEnsureAdditionalFilesConfigMap(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()
	cmKey := client.ObjectKey{Namespace: "ns", Name: "j-nixfiles"}
	owner := &niov1alpha1.NixJob{ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: "ns", UID: "u"}}

	inline := "hello"
	nix := niov1alpha1.NixSpec{AdditionalFiles: []niov1alpha1.NixFile{
		{Path: "a.nix", Inline: &inline},
		{Path: "b.nix", ConfigMapRef: &niov1alpha1.ConfigMapKeyReference{Name: "cm", Key: "k"}},
	}}

	if err := ensureAdditionalFilesConfigMap(ctx, c, scheme, owner, nix); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	var cm corev1.ConfigMap
	if err := c.Get(ctx, cmKey, &cm); err != nil {
		t.Fatalf("configmap not created: %v", err)
	}
	// Only the inline file (index 0) is stored; the configMapRef file is not.
	if cm.Data["file-0"] != "hello" {
		t.Errorf("inline content = %q, want hello", cm.Data["file-0"])
	}
	if _, ok := cm.Data["file-1"]; ok {
		t.Error("non-inline file must not be stored in the nixfiles ConfigMap")
	}
	if len(cm.OwnerReferences) == 0 {
		t.Error("nixfiles ConfigMap should be owned by the workload")
	}

	// Dropping the inline file removes the now-stale ConfigMap.
	if err := ensureAdditionalFilesConfigMap(ctx, c, scheme, owner, niov1alpha1.NixSpec{}); err != nil {
		t.Fatalf("ensure empty: %v", err)
	}
	if err := c.Get(ctx, cmKey, &cm); !apierrors.IsNotFound(err) {
		t.Errorf("stale nixfiles ConfigMap not deleted: err=%v", err)
	}
}
