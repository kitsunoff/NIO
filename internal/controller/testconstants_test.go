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

// Fixture values shared by the controller tests.
//
// Every name here is prefixed with "test" so that it can never collide with a
// production constant in this package.
//
// Several values are spelled identically but mean different things; those keep
// SEPARATE constants on purpose, because changing one meaning must not silently
// change the other. Each such pair is called out in a comment below.
const (
	// --- Namespaces -------------------------------------------------------

	// testNamespaceDefault is the namespace the envtest and fake-client
	// fixtures create their objects in.
	testNamespaceDefault = "default"
	// testNamespaceApps is the workload namespace used by the resolve/watch
	// fixtures.
	testNamespaceApps = "apps"
	// testNamespaceInfra is the namespace the NixCluster / NixosConfiguration
	// fixtures live in.
	testNamespaceInfra = "infra"
	// testNamespaceProd is a SECOND namespace, used only to prove that a
	// mapper/index does not cross namespace boundaries. Same spelling as
	// testNameProd below, different meaning: this one is a namespace.
	testNamespaceProd = "prod"

	// --- Object names -----------------------------------------------------

	// testNameWeb is the name of the NixosConfiguration / workload under test.
	testNameWeb = "web"
	// testFluxSourceWeb is the name of the Flux GitRepository object the
	// resolver fixtures create and reference. Same spelling as testNameWeb,
	// different meaning: this one names a Flux source, not the workload.
	testFluxSourceWeb = "web"
	// testNameProd is the name of the NixCluster under test. Same spelling as
	// testNamespaceProd, different meaning: this one is an object name.
	testNameProd = "prod"
	// testNameStore is the name of the NixStore object the fixtures create and
	// reference through storeRef.
	testNameStore = "store"
	// testNameCacheStore is a second NixStore name, used where a test needs a
	// store reference distinguishable from testNameStore.
	testNameCacheStore = "cache-store"
	// testNameBuilder is the name of the NixBuilder object referenced through
	// builderRef.
	testNameBuilder = "builder"
	// testNameNightly is the name shared by the NixCronJob fixture and the
	// batch/v1 CronJob it projects.
	testNameNightly = "nightly"
	// testNameWebInstall, testNameWebDayTwo and testNameWebOnRemove are the
	// child object names the NixosConfiguration controller derives from
	// testNameWeb.
	testNameWebInstall  = "web-install"
	testNameWebDayTwo   = "web-day2"
	testNameWebOnRemove = "web-onremove"

	// testSecretNameClusterSSH is the NixCluster spec.sshKeyRef Secret name.
	testSecretNameClusterSSH = "cluster-ssh"
	// testSecretNameStoreSSH is the resolved store/builder SSH Secret name
	// nixrender mounts into the pod.
	testSecretNameStoreSSH = "store-ssh"
	// testSecretNameToken is the NAME of a git-credentials Secret that carries
	// a token. Same spelling as testSecretKeyToken, different meaning: this one
	// is an object name.
	testSecretNameToken = "token"

	// --- Container names --------------------------------------------------

	// testContainerStore is the name of the container inside the NixStore
	// StatefulSet. Same spelling as testNameStore, different meaning: this one
	// is a container name, not the NixStore object's name.
	testContainerStore = "store"

	// --- Machines ---------------------------------------------------------

	// testMachineM00..testMachineM08 are Machine names for the NixCluster
	// selection tests; the algorithm sorts on them, so the values matter.
	testMachineM00 = "m-00"
	testMachineM01 = "m-01"
	testMachineM02 = "m-02"
	testMachineM03 = "m-03"
	testMachineM04 = "m-04"
	testMachineM05 = "m-05"
	testMachineM08 = "m-08"
	// testMachineNode01 is the Machine name used by the node-file and
	// state-machine fixtures.
	testMachineNode01 = "node-01"
	// testMachineHost is the Machine's spec.host (an address literal).
	testMachineHost = "10.0.0.5"
	// testMachineHostFQDN is the Machine's spec.host in the machine-controller
	// fixtures, which exercise a DNS name rather than an address.
	testMachineHostFQDN = "test-host.example.com"
	// testSSHUserRoot is the Machine's spec.sshUser.
	testSSHUserRoot = "root"
	// testTargetHost is the user@host argument nixos-rebuild/nixos-anywhere
	// receive; it is testSSHUserRoot at testMachineHost.
	testTargetHost = "root@10.0.0.5"

	// --- Labels -----------------------------------------------------------

	// testLabelRole and testLabelTier are Machine label KEYS the NixCluster
	// nodeGroup selectors match on.
	testLabelRole = "role"
	testLabelTier = "tier"
	// testRoleServer and testRoleWorker are the VALUES of the testLabelRole
	// label.
	testRoleServer = "server"
	testRoleWorker = "worker"

	// --- Git sources ------------------------------------------------------

	testRepoExampleR    = "https://example.com/r"
	testRepoExampleProd = "https://example.com/prod"
	testRepoX           = "https://x/r"
	testRepoAcmeWeb     = "https://github.com/acme/web"
	// testDirHostsWeb is spec.source.dir — the flake subdirectory.
	testDirHostsWeb = "hosts/web"

	// --- Revisions --------------------------------------------------------
	//
	// These are fixture commit-ish values. They are named after their literal
	// spelling so that no test can accidentally be switched to a different
	// revision while still reading correctly.

	testRevAbc              = "abc"
	testRevAbc123           = "abc123"
	testRevAbc1234          = "abc1234"
	testRevAbcdef1          = "abcdef1"
	testRevAbcdef1234567890 = "abcdef1234567890"
	// testRevUnused is the SHA of a fake git resolver that the test asserts is
	// never consulted, because the source pins a revision.
	testRevUnused = "unused"

	// --- Nix --------------------------------------------------------------

	testNixSystemX8664   = "x86_64-linux"
	testNixSystemAarch64 = "aarch64-linux"
	// testRunServer and testRunX are spec.nix.run installables.
	testRunServer = ".#server"
	testRunX      = ".#x"
	// testCmdNix and testCmdRun are the leading words of the command the app
	// container is expected to run.
	testCmdNix = "nix"
	testCmdRun = "run"
	// testArg* are command-line arguments in expected argv slices.
	testArgPort       = "--port"
	testArgPortValue  = "8080"
	testArgFlake      = "--flake"
	testArgSSHOption  = "--ssh-option"
	testArgTargetHost = "--target-host"
	testArgSwitch     = "switch"
	testArgConverge   = "converge"
	// testImageNixosNix is the container image the fixtures set on spec.nix.image.
	testImageNixosNix = "nixos/nix"
	// testBuilderEndpoint is a resolved NixBuilder ssh-ng endpoint.
	testBuilderEndpoint = "ssh-ng://root@b.svc"
	// testBuildersLinePrefix is the start of the NIX_CONFIG "builders =" line
	// produced for testBuilderEndpoint, including its trailing space.
	testBuildersLinePrefix = "builders = ssh-ng://root@b.svc "

	// --- Environment variables --------------------------------------------

	testEnvNixConfig  = "NIX_CONFIG"
	testEnvNixSSHOpts = "NIX_SSHOPTS"
	testEnvNioGitRepo = "NIO_GIT_REPO"

	// --- Secret data keys -------------------------------------------------

	// testSecretKeyToken is the KEY inside a Secret holding a git token. Same
	// spelling as testSecretNameToken, different meaning: this one is a data
	// key, not an object name.
	testSecretKeyToken = "token"
	// testSecretKeyPassword is the KEY inside a git-credentials Secret holding
	// the password.
	testSecretKeyPassword = "password"

	// --- API kinds --------------------------------------------------------

	// testKindGitRepository is the Flux source kind referenced by
	// spec.source.fluxSourceRef.
	testKindGitRepository = "GitRepository"

	// --- Status reasons ---------------------------------------------------

	// testReasonError is a terminated-container Reason on a Pod status.
	testReasonError = "Error"
	// testReasonBackoffLimitExceeded is a batch/v1 Job condition Reason.
	testReasonBackoffLimitExceeded = "BackoffLimitExceeded"

	// --- Table-test case labels -------------------------------------------
	//
	// These name the three NixosConfiguration children in table-driven tests.
	// testChildDecommission deliberately does NOT reuse the production
	// operationDecommission constant: that one is the value of the
	// nio.homystack.com/operation label, whereas these three are test-case
	// labels that happen to spell the same words.
	testChildInstall      = "install"
	testChildDayTwo       = "day2"
	testChildDecommission = "decommission"

	// --- Generated-name suffixes ------------------------------------------

	// testSigningKeySecretSuffix is appended to a NixStore's name to form the
	// generated signing-key Secret's name.
	testSigningKeySecretSuffix = "-signing-key"
	// testStorePublicKeySuffix is appended to a NixStore's name to form a
	// plausible status.publicKey value.
	testStorePublicKeySuffix = "-1:AAAA"
)
