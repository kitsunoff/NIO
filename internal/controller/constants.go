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

// This file names the string literals the controllers repeat. Each constant is
// scoped to one meaning: identical spellings that belong to different contracts
// (two CLIs that happen to share a flag name, a container name versus a log key)
// deliberately stay separate, so changing one never silently changes the other.

// Default SSH identity for a Machine that does not pin spec.sshUser. Used both
// by the connectivity probe and when addressing the target host of an apply.
const defaultSSHUser = "root"

// Environment variables read by the nix CLI inside NIO-managed pods.
const (
	// envNixConfig carries inline nix.conf settings (substituters, builders,
	// experimental features) into a pod.
	envNixConfig = "NIX_CONFIG"

	// envNixSSHOpts carries the ssh options nix passes to its own ssh calls
	// (remote-build dispatch, ssh-ng store pushes, target-host applies).
	envNixSSHOpts = "NIX_SSHOPTS"

	// nixConfigExperimentalFeatures is the nix.conf line enabling the flake
	// workflow. Every NIO pod that shells out to `nix` needs it.
	nixConfigExperimentalFeatures = "experimental-features = nix-command flakes"
)

// nix CLI vocabulary. Only the executable name and the `run` subcommand repeat
// across the package; `build`, `shell` and `copy` each have a single call site
// and stay literal rather than growing a constant per subcommand.
const (
	nixBinary = "nix"
	nixCmdRun = "run"
)

// nix system doubles NIO supports. These are nix's own platform identifiers, not
// Kubernetes or Go architecture names.
const (
	nixSystemX8664Linux   = "x86_64-linux"
	nixSystemAarch64Linux = "aarch64-linux"
)

// Container names inside the infrastructure StatefulSets. These are Kubernetes
// object field values: renaming one rewrites the pod spec.
const (
	builderContainerName = "builder"
	storeContainerName   = "store"
)

// Kubernetes Service/ContainerPort names. A Service port name and the container
// port name it targets must agree, so both sides use these.
const (
	portNameSSH  = "ssh"
	portNameHTTP = "http"
)

// nixos-anywhere CLI surface, used by the one-shot install child. nixos-anywhere
// runs its own ssh and does not honor NIX_SSHOPTS, hence the explicit flags.
const (
	anywhereFlagFlake     = "--flake"
	anywhereFlagSSHOption = "--ssh-option"
)

// nixos-rebuild CLI surface, used by the day-2 and decommission children. Kept
// separate from the nixos-anywhere flags above: the two tools are independent
// programs that merely happen to spell --flake the same way today.
const (
	rebuildArgSwitch      = "switch"
	rebuildFlagFlake      = "--flake"
	rebuildFlagTargetHost = "--target-host"
)

// clusterConvergeArg is the argument handed to a NixCluster's own app
// installable to run one convergence pass. Unrelated to convergeChildSuffix,
// which names the owned NixCronJob.
const clusterConvergeArg = "converge"

// Kubernetes container-state reasons that mean the init build is not making
// progress.
const (
	containerReasonError            = "Error"
	containerReasonCrashLoopBackOff = "CrashLoopBackOff"
)

// Flux source kinds a workload may reference, in the fluxSourceGroup API group.
const (
	kindGitRepository = "GitRepository"
	kindOCIRepository = "OCIRepository"
	kindBucket        = "Bucket"
)
