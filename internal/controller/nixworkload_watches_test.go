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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

func TestEnqueueByCredentialsSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	credRef := func(name string) *niov1alpha1.SecretReference { return &niov1alpha1.SecretReference{Name: name} }
	wantsSecret := &niov1alpha1.NixDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
		Spec:       niov1alpha1.NixDeploymentSpec{Nix: niov1alpha1.NixSpec{Source: niov1alpha1.NixSource{CredentialsRef: credRef("git-creds")}}},
	}
	otherSecret := &niov1alpha1.NixDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps"},
		Spec:       niov1alpha1.NixDeploymentSpec{Nix: niov1alpha1.NixSpec{Source: niov1alpha1.NixSource{CredentialsRef: credRef("other")}}},
	}
	otherNS := &niov1alpha1.NixDeployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod"},
		Spec:       niov1alpha1.NixDeploymentSpec{Nix: niov1alpha1.NixSpec{Source: niov1alpha1.NixSource{CredentialsRef: credRef("git-creds")}}},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(wantsSecret, otherSecret, otherNS).
		WithIndex(&niov1alpha1.NixDeployment{}, IndexByCredentialsSecret, func(o client.Object) []string {
			src := o.(*niov1alpha1.NixDeployment).Spec.Nix.Source
			if src.CredentialsRef == nil {
				return nil
			}
			return []string{src.CredentialsRef.Name}
		}).
		Build()

	mapFn := enqueueByIndex(c, &niov1alpha1.NixDeploymentList{}, IndexByCredentialsSecret)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git-creds", Namespace: "apps"}}
	reqs := mapFn(context.Background(), secret)

	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request (same-namespace referencing workload), got %d: %v", len(reqs), reqs)
	}
	if reqs[0].Name != "web" || reqs[0].Namespace != "apps" {
		t.Errorf("unexpected request %v", reqs[0])
	}
}
