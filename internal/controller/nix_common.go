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
	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

// Default images used by the NIO infrastructure controllers. The store server
// image is a Nix HTTP binary cache (harmonia); the exact serving command is
// validated end-to-end on Kind (design O8 / ADR-0006).
const (
	// DefaultNixStoreImage is the default NixStore server image. It is a
	// nix-bearing image; the controller runs the harmonia binary cache from
	// nixpkgs inside it (there is no maintained standalone harmonia OCI image).
	DefaultNixStoreImage = "nixos/nix:latest"

	// DefaultNixBuilderImage is the default NixBuilder worker image.
	DefaultNixBuilderImage = "nixos/nix:latest"

	// DefaultRunnerImage is the default nix-bearing runner image for workloads.
	DefaultRunnerImage = "nixos/nix:latest"

	// NixStoreHTTPPort is the port the store server serves the binary cache on.
	NixStoreHTTPPort = 5000

	// NixStoreSSHPort is the port the store's nix-daemon accepts ssh-ng pushes on.
	NixStoreSSHPort = 22

	// NixBuilderSSHPort is the port the builder accepts remote builds on.
	NixBuilderSSHPort = 22
)

// managedLabels returns the standard labels stamped on every NIO-managed object
// for the given workload/infra kind and name.
func managedLabels(kind, name string) map[string]string {
	return map[string]string{
		niov1alpha1.LabelWorkloadKind: kind,
		niov1alpha1.LabelWorkloadName: name,
		niov1alpha1.LabelManagedBy:    niov1alpha1.ManagedByValue,
	}
}
