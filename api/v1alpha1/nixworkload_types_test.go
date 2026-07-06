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

package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

// TestSchemeRegistration ensures every new Nix workload and infra kind (and its
// List) is registered in the scheme, so the manager can serve them.
func TestSchemeRegistration(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}

	objects := []runtime.Object{
		&NixStore{}, &NixStoreList{},
		&NixBuilder{}, &NixBuilderList{},
		&NixDeployment{}, &NixDeploymentList{},
		&NixJob{}, &NixJobList{},
		&NixCronJob{}, &NixCronJobList{},
		&NixStatefulSet{}, &NixStatefulSetList{},
	}
	for _, obj := range objects {
		gvks, _, err := scheme.ObjectKinds(obj)
		if err != nil {
			t.Errorf("ObjectKinds(%T) failed: %v", obj, err)
			continue
		}
		if len(gvks) == 0 {
			t.Errorf("%T is not registered in the scheme", obj)
			continue
		}
		if gvks[0].Group != "nio.homystack.com" || gvks[0].Version != "v1alpha1" {
			t.Errorf("%T registered under unexpected GVK %v", obj, gvks[0])
		}
	}
}

// TestDeepCopyRoundTrip exercises the generated DeepCopy for the workload kinds,
// including the embedded native specs and the NixSpec references, to guard
// against a missing deepcopy for a newly added field.
func TestDeepCopyRoundTrip(t *testing.T) {
	trigger := true
	dep := &NixDeployment{
		Spec: NixDeploymentSpec{
			Nix: NixSpec{
				Source: NixSource{
					GitRepo:        "https://github.com/acme/web",
					Ref:            "main",
					CredentialsRef: &SecretReference{Name: "creds"},
					FluxSourceRef:  &FluxSourceRef{Kind: "GitRepository", Name: "web"},
				},
				Run:             ".#server",
				Args:            []string{"--port", "8080"},
				Prebuild:        []string{".#dep"},
				StoreRef:        &LocalObjectReference{Name: "store"},
				BuilderRef:      &LocalObjectReference{Name: "builder"},
				TriggerOnChange: &trigger,
				LocalStore:      &NixLocalStore{Medium: "Disk"},
			},
		},
	}
	out := dep.DeepCopy()
	if out == dep {
		t.Fatal("DeepCopy returned the same pointer")
	}
	if out.Spec.Nix.Run != dep.Spec.Nix.Run {
		t.Errorf("DeepCopy did not copy Run: got %q", out.Spec.Nix.Run)
	}
	// Mutating the copy must not affect the original (deep independence).
	out.Spec.Nix.Args[0] = "--mutated"
	if dep.Spec.Nix.Args[0] == "--mutated" {
		t.Error("DeepCopy shared the Args slice with the original")
	}
	out.Spec.Nix.StoreRef.Name = "other"
	if dep.Spec.Nix.StoreRef.Name == "other" {
		t.Error("DeepCopy shared the StoreRef pointer with the original")
	}
}
