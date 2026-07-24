//go:build e2e
// +build e2e

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

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kitsunoff/nixos-operator/test/utils"
)

// orchNamespace is a dedicated, non-PSS-restricted namespace for the
// NixosConfiguration orchestrator scenarios. It is deliberately separate from
// the "nio-workloads" namespace used by the Nix workloads Describe so the two
// Ordered containers do not share a namespace lifecycle. A freshly created
// namespace carries no pod-security label, so it is unrestricted by default —
// which is what the SSH-fixture and child Nix pods need.
const orchNamespace = "nio-orch-e2e"

// Shared SSH fixture identifiers.
const (
	orchSSHSecretName = "nio-target-ssh-key" // holds ssh-privatekey
	orchSSHDName      = "nio-sshd"           // Deployment + Service name
	orchSSHDUser      = "nio"                // login user configured in the sshd
)

// finalizerName mirrors api/v1alpha1.FinalizerName. It is duplicated as a
// string literal here rather than imported so the e2e package stays decoupled
// from the api module's constants (the value is part of the observable
// contract that this suite pins).
const finalizerName = "nio.homystack.com/finalizer"

// kgetIn runs `kubectl get -n <ns> ...` and returns trimmed stdout, or "" on
// any error (e.g. NotFound). It complements the workloads-scoped kget helper
// so this file can target its own namespace.
func kgetIn(ns string, args ...string) string {
	full := append([]string{"get", "-n", ns}, args...)
	out, err := utils.Run(exec.Command("kubectl", full...))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// kdelete deletes a resource in orchNamespace best-effort (never fails the
// caller).
func kdelete(kind, name string, extra ...string) {
	args := append([]string{"delete", kind, name, "-n", orchNamespace, "--ignore-not-found=true"}, extra...)
	_, _ = utils.Run(exec.Command("kubectl", args...))
}

// awaitMachineDiscoverable polls the Machine's status.discoverable until it is
// "true" or the timeout elapses. It returns whether the machine became
// discoverable WITHOUT failing the test, so callers can Skip cleanly when the
// in-cluster sshd fixture cannot make a machine reachable.
func awaitMachineDiscoverable(ns, name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if kgetIn(ns, "machine", name, "-o", "jsonpath={.status.discoverable}") == "true" {
			return true
		}
		time.Sleep(5 * time.Second)
	}
	return false
}

var _ = Describe("NixosConfiguration orchestrator (tier-2)", Ordered, func() {
	// nioRev pins a real, fetchable commit so children reference an immutable
	// SHA rather than a moving branch (the operator image is distroless/no-git).
	// It is NOT load-bearing for these assertions — every tier-2 check fires on
	// child CREATION + SHAPE + orchestrator PHASE, which do not depend on the
	// child pods actually fetching or applying anything.
	var nioRev string

	// sshdUp records whether the in-cluster sshd fixture came up. When false,
	// the discoverable-path (HIGH-VALUE) scenarios Skip rather than fail; the
	// MANDATORY scenarios (S1-S4) need no host and always run.
	var sshdUp bool

	// dayTwoConfig renders a non-fullInstall NixosConfiguration referencing the
	// given machine. gitRepo/ref are pinned but irrelevant to the assertions.
	dayTwoConfig := func(name, machine string, extraSpec string) string {
		return `
apiVersion: nio.homystack.com/v1alpha1
kind: NixosConfiguration
metadata: {name: ` + name + `, namespace: ` + orchNamespace + `}
spec:
  machineRef: {name: ` + machine + `}
  gitRepo: "https://github.com/kitsunoff/NIO"
  ref: "` + nioRev + `"
  flake: "#web"
  dayTwoSchedule: "*/30 * * * *"` + extraSpec
	}

	// machineManifest renders a Machine pointing at the shared sshd Service.
	machineManifest := func(name string) string {
		return `
apiVersion: nio.homystack.com/v1alpha1
kind: Machine
metadata: {name: ` + name + `, namespace: ` + orchNamespace + `}
spec:
  host: ` + orchSSHDName + `.` + orchNamespace + `.svc.cluster.local
  sshUser: ` + orchSSHDUser + `
  sshKeySecretRef: {name: ` + orchSSHSecretName + `}`
	}

	// setupSSHDFixture generates an ed25519 keypair, stores the PRIVATE key in a
	// Secret (key "ssh-privatekey") referenced by the Machines, and deploys an
	// in-cluster sshd whose authorized_keys is the PUBLIC key — so the operator's
	// authenticated SSH dial (ssh.NewClientConn does a full public-key handshake)
	// succeeds and the Machine flips to status.discoverable=true. Best-effort:
	// returns an error instead of failing the suite.
	setupSSHDFixture := func() error {
		tmp, err := os.MkdirTemp("", "nio-orch-ssh-*")
		if err != nil {
			return fmt.Errorf("mkdir temp: %w", err)
		}
		keyPath := filepath.Join(tmp, "id")
		if _, err := utils.Run(exec.Command("ssh-keygen",
			"-t", "ed25519", "-N", "", "-C", "nio-e2e", "-f", keyPath)); err != nil {
			return fmt.Errorf("ssh-keygen: %w", err)
		}
		pubBytes, err := os.ReadFile(keyPath + ".pub")
		if err != nil {
			return fmt.Errorf("read public key: %w", err)
		}
		pubKey := strings.TrimSpace(string(pubBytes))

		// Store the private key in a Secret (from file preserves exact bytes).
		if _, err := utils.Run(exec.Command("kubectl", "create", "secret", "generic",
			orchSSHSecretName, "-n", orchNamespace,
			"--from-file=ssh-privatekey="+keyPath)); err != nil {
			return fmt.Errorf("create ssh secret: %w", err)
		}

		// Deploy sshd (linuxserver/openssh-server): PUBLIC_KEY becomes the user's
		// authorized_keys, USER_NAME is the login user, and it listens on 2222.
		// The Service publishes port 22 (what the operator dials) -> 2222.
		applyYAML(`
apiVersion: apps/v1
kind: Deployment
metadata: {name: ` + orchSSHDName + `, namespace: ` + orchNamespace + `}
spec:
  replicas: 1
  selector: {matchLabels: {app: ` + orchSSHDName + `}}
  template:
    metadata: {labels: {app: ` + orchSSHDName + `}}
    spec:
      containers:
      - name: sshd
        image: lscr.io/linuxserver/openssh-server:latest
        env:
        - {name: PUID, value: "1000"}
        - {name: PGID, value: "1000"}
        - {name: USER_NAME, value: "` + orchSSHDUser + `"}
        - {name: PASSWORD_ACCESS, value: "false"}
        - {name: SUDO_ACCESS, value: "false"}
        - {name: PUBLIC_KEY, value: "` + pubKey + `"}
        ports:
        - {containerPort: 2222}
---
apiVersion: v1
kind: Service
metadata: {name: ` + orchSSHDName + `, namespace: ` + orchNamespace + `}
spec:
  selector: {app: ` + orchSSHDName + `}
  ports:
  - {name: ssh, port: 22, targetPort: 2222}`)

		if _, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
			"deploy/"+orchSSHDName, "-n", orchNamespace, "--timeout=180s")); err != nil {
			return fmt.Errorf("sshd deployment not Available: %w", err)
		}
		return nil
	}

	BeforeAll(func() {
		By("ensuring the controller-manager is Available")
		_, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
			"deploy/go-operator-controller-manager", "-n", namespace, "--timeout=180s"))
		Expect(err).NotTo(HaveOccurred())

		By("creating the orchestrator namespace")
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", orchNamespace))

		By("resolving a pinned source revision (best-effort)")
		out, lsErr := utils.Run(exec.Command("git", "ls-remote",
			"https://github.com/kitsunoff/NIO.git", "refs/heads/main"))
		if lsErr == nil && len(strings.Fields(out)) > 0 {
			nioRev = strings.Fields(out)[0]
		} else {
			// Non-fatal: the ref value does not affect child creation or phase.
			nioRev = "main"
		}

		By("bringing up the in-cluster sshd fixture (best-effort)")
		if err := setupSSHDFixture(); err != nil {
			_, _ = fmt.Fprintf(GinkgoWriter,
				"sshd fixture unavailable, HIGH-VALUE scenarios will Skip: %v\n", err)
			sshdUp = false
		} else {
			sshdUp = true
		}
	})

	AfterAll(func() {
		By("removing the orchestrator namespace")
		// Drop finalizers on any lingering configs so namespace deletion is not
		// wedged by a decommission that cannot complete against the fake host.
		names := kgetIn(orchNamespace, "nixosconfiguration",
			"-o", "jsonpath={.items[*].metadata.name}")
		for _, n := range strings.Fields(names) {
			_, _ = utils.Run(exec.Command("kubectl", "patch", "nixosconfiguration", n,
				"-n", orchNamespace, "--type=merge",
				"-p", `{"metadata":{"finalizers":[]}}`))
		}
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", orchNamespace, "--wait=false"))
	})

	// ---------------------------------------------------------------------
	// MANDATORY scenarios — need no host, must pass reliably.
	// ---------------------------------------------------------------------

	It("[MANDATORY] Blocks when the referenced Machine is missing", func() {
		const cfg = "s1-cfg"
		DeferCleanup(func() { kdelete("nixosconfiguration", cfg, "--wait=false") })

		applyYAML(dayTwoConfig(cfg, "s1-missing-machine", ""))

		By("the config reaches the Blocked phase")
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixosconfiguration", cfg, "-o", "jsonpath={.status.phase}")
		}, 3*time.Minute, 5*time.Second).Should(Equal("Blocked"))

		By("the Ready condition is False")
		Expect(kgetIn(orchNamespace, "nixosconfiguration", cfg,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")).To(Equal("False"))

		By("no install/day-2 children are created")
		Expect(kgetIn(orchNamespace, "nixcronjob", cfg+"-day2",
			"-o", "jsonpath={.metadata.name}")).To(BeEmpty())
		Expect(kgetIn(orchNamespace, "nixjob", cfg+"-install",
			"-o", "jsonpath={.metadata.name}")).To(BeEmpty())
	})

	It("[MANDATORY] Blocks when the Machine is not discoverable", func() {
		const (
			cfg     = "s2-cfg"
			machine = "s2-machine"
		)
		DeferCleanup(func() {
			kdelete("nixosconfiguration", cfg, "--wait=false")
			kdelete("machine", machine, "--wait=false")
		})

		By("creating a Machine pointing at an unreachable host (TEST-NET-1)")
		// 192.0.2.10 is RFC 5737 TEST-NET-1: guaranteed non-routable, so the
		// operator's SSH dial never succeeds and discoverable stays false. No
		// sshKeySecretRef is needed to keep this scenario independent of the
		// sshd fixture.
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: Machine
metadata: {name: ` + machine + `, namespace: ` + orchNamespace + `}
spec:
  host: 192.0.2.10
  sshUser: root`)

		applyYAML(dayTwoConfig(cfg, machine, ""))

		By("the config reaches the Blocked phase")
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixosconfiguration", cfg, "-o", "jsonpath={.status.phase}")
		}, 3*time.Minute, 5*time.Second).Should(Equal("Blocked"))

		Expect(kgetIn(orchNamespace, "nixosconfiguration", cfg,
			"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")).To(Equal("False"))

		By("no children are created")
		Expect(kgetIn(orchNamespace, "nixcronjob", cfg+"-day2",
			"-o", "jsonpath={.metadata.name}")).To(BeEmpty())
		Expect(kgetIn(orchNamespace, "nixjob", cfg+"-install",
			"-o", "jsonpath={.metadata.name}")).To(BeEmpty())
	})

	It("[MANDATORY] Blocks the second config that claims an owned Machine (uniqueness gate)", func() {
		const (
			owner = "s3-cfg-a" // earliest-created + lexicographically smaller -> owns
			loser = "s3-cfg-b"
		)
		DeferCleanup(func() {
			kdelete("nixosconfiguration", owner, "--wait=false")
			kdelete("nixosconfiguration", loser, "--wait=false")
		})

		By("creating the owning config first, then (after a beat) the second")
		// The uniqueness gate runs BEFORE the machine gate, so this holds even
		// though s3-machine does not exist. s3-cfg-a wins ownership (earlier
		// creationTimestamp, and 'a' < 'b' on any tie); s3-cfg-b is the loser.
		applyYAML(dayTwoConfig(owner, "s3-machine", ""))
		time.Sleep(2 * time.Second)
		applyYAML(dayTwoConfig(loser, "s3-machine", ""))

		By("the second config is Blocked with a MachineInUse Stalled condition")
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixosconfiguration", loser, "-o", "jsonpath={.status.phase}")
		}, 3*time.Minute, 5*time.Second).Should(Equal("Blocked"))

		Eventually(func() string {
			return kgetIn(orchNamespace, "nixosconfiguration", loser,
				"-o", "jsonpath={.status.conditions[?(@.type=='Stalled')].status}")
		}, 1*time.Minute, 5*time.Second).Should(Equal("True"))

		Expect(kgetIn(orchNamespace, "nixosconfiguration", loser,
			"-o", "jsonpath={.status.conditions[?(@.type=='Stalled')].reason}")).To(Equal("MachineInUse"))
		Expect(kgetIn(orchNamespace, "nixosconfiguration", loser,
			"-o", "jsonpath={.status.conditions[?(@.type=='Stalled')].message}")).To(ContainSubstring("already owned"))

		By("the blocked config drives no children")
		Expect(kgetIn(orchNamespace, "nixcronjob", loser+"-day2",
			"-o", "jsonpath={.metadata.name}")).To(BeEmpty())
		Expect(kgetIn(orchNamespace, "nixjob", loser+"-install",
			"-o", "jsonpath={.metadata.name}")).To(BeEmpty())
	})

	It("[MANDATORY] Adds the finalizer and deletes cleanly with no onRemoveFlake", func() {
		const cfg = "s4-cfg"
		// No DeferCleanup delete: the test deletes the config itself. A guard is
		// still registered in case an assertion fails mid-way.
		DeferCleanup(func() { kdelete("nixosconfiguration", cfg, "--wait=false") })

		applyYAML(dayTwoConfig(cfg, "s4-missing-machine", ""))

		By("the orchestrator finalizer is present")
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixosconfiguration", cfg,
				"-o", "jsonpath={.metadata.finalizers[0]}")
		}, 2*time.Minute, 5*time.Second).Should(Equal(finalizerName))

		By("deleting the config removes the finalizer and the object is gone")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "nixosconfiguration", cfg,
			"-n", orchNamespace, "--wait=false"))

		Eventually(func() string {
			// A present object echoes its name; NotFound yields "".
			return kgetIn(orchNamespace, "nixosconfiguration", cfg, "-o", "jsonpath={.metadata.name}")
		}, 3*time.Minute, 5*time.Second).Should(BeEmpty(), "config was not fully deleted")
	})

	// ---------------------------------------------------------------------
	// HIGH-VALUE scenarios — need the in-cluster sshd fixture. Each Skips
	// cleanly if the machine cannot be made discoverable.
	// ---------------------------------------------------------------------

	It("[HIGH-VALUE:sshd] Converges a discoverable Machine and creates an owned day-2 NixCronJob", func() {
		if !sshdUp {
			Skip("in-cluster sshd fixture is not available")
		}
		const (
			cfg     = "s5-cfg"
			machine = "s5-machine"
		)
		DeferCleanup(func() {
			kdelete("nixosconfiguration", cfg, "--wait=false")
			kdelete("machine", machine, "--wait=false")
		})

		By("creating a Machine and waiting for it to become discoverable")
		applyYAML(machineManifest(machine))
		if !awaitMachineDiscoverable(orchNamespace, machine, 5*time.Minute) {
			Skip("Machine did not become discoverable against the in-cluster sshd")
		}

		By("creating a non-fullInstall config")
		applyYAML(dayTwoConfig(cfg, machine, ""))

		By("the owned day-2 NixCronJob is created")
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixcronjob", cfg+"-day2", "-o", "jsonpath={.metadata.name}")
		}, 3*time.Minute, 5*time.Second).Should(Equal(cfg + "-day2"))

		By("the day-2 cron is owned by the config")
		Expect(kgetIn(orchNamespace, "nixcronjob", cfg+"-day2",
			"-o", "jsonpath={.metadata.ownerReferences[0].name}")).To(Equal(cfg))

		By("the day-2 cron has the expected shape (Forbid + triggerOnChange)")
		Expect(kgetIn(orchNamespace, "nixcronjob", cfg+"-day2",
			"-o", "jsonpath={.spec.cronJobTemplate.concurrencyPolicy}")).To(Equal("Forbid"))
		Expect(kgetIn(orchNamespace, "nixcronjob", cfg+"-day2",
			"-o", "jsonpath={.spec.nix.triggerOnChange}")).To(Equal("true"))

		By("the config phase advances into the day-2 lifecycle")
		// Converging is the target; Ready/Degraded are also accepted because the
		// child cron runs nixos-rebuild against a NON-NixOS sshd and will not
		// actually converge. All three prove the machine + uniqueness gates
		// passed and the orchestrator is driving the day-2 child.
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixosconfiguration", cfg, "-o", "jsonpath={.status.phase}")
		}, 3*time.Minute, 5*time.Second).Should(BeElementOf("Converging", "Ready", "Degraded"))
	})

	It("[HIGH-VALUE:sshd] Installs a discoverable Machine and creates an owned install NixJob", func() {
		if !sshdUp {
			Skip("in-cluster sshd fixture is not available")
		}
		const (
			cfg     = "s6-cfg"
			machine = "s6-machine"
		)
		DeferCleanup(func() {
			kdelete("nixosconfiguration", cfg, "--wait=false")
			kdelete("machine", machine, "--wait=false")
		})

		By("creating a Machine and waiting for it to become discoverable")
		applyYAML(machineManifest(machine))
		if !awaitMachineDiscoverable(orchNamespace, machine, 5*time.Minute) {
			Skip("Machine did not become discoverable against the in-cluster sshd")
		}

		By("creating a fullInstall config")
		applyYAML(dayTwoConfig(cfg, machine, "\n  fullInstall: true"))

		By("the owned install NixJob is created")
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixjob", cfg+"-install", "-o", "jsonpath={.metadata.name}")
		}, 3*time.Minute, 5*time.Second).Should(Equal(cfg + "-install"))

		By("the install NixJob is owned by the config")
		Expect(kgetIn(orchNamespace, "nixjob", cfg+"-install",
			"-o", "jsonpath={.metadata.ownerReferences[0].name}")).To(Equal(cfg))

		By("the config phase is Installing (or Degraded once the fake install exhausts retries)")
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixosconfiguration", cfg, "-o", "jsonpath={.status.phase}")
		}, 3*time.Minute, 5*time.Second).Should(BeElementOf("Installing", "Degraded"))
	})

	It("[HIGH-VALUE:sshd] Creates an orphan decommission NixJob when deleted with onRemoveFlake", func() {
		if !sshdUp {
			Skip("in-cluster sshd fixture is not available")
		}
		const (
			cfg     = "s7-cfg"
			machine = "s7-machine"
		)
		DeferCleanup(func() {
			// The decommission cannot complete against the fake host within the
			// test window, so the finalizer would wedge the delete. Drop it, then
			// clean up the orphan NixJob (no ownerRef -> survives) and the machine.
			_, _ = utils.Run(exec.Command("kubectl", "patch", "nixosconfiguration", cfg,
				"-n", orchNamespace, "--type=merge",
				"-p", `{"metadata":{"finalizers":[]}}`))
			kdelete("nixosconfiguration", cfg, "--wait=false")
			kdelete("nixjob", cfg+"-onremove", "--wait=false")
			kdelete("machine", machine, "--wait=false")
		})

		By("creating a Machine and waiting for it to become discoverable")
		applyYAML(machineManifest(machine))
		if !awaitMachineDiscoverable(orchNamespace, machine, 5*time.Minute) {
			Skip("Machine did not become discoverable against the in-cluster sshd")
		}

		By("creating a config with an onRemoveFlake")
		applyYAML(dayTwoConfig(cfg, machine, "\n  onRemoveFlake: \"#decommission\""))

		By("the config is orchestrating (finalizer present, day-2 cron created)")
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixosconfiguration", cfg,
				"-o", "jsonpath={.metadata.finalizers[0]}")
		}, 2*time.Minute, 5*time.Second).Should(Equal(finalizerName))
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixcronjob", cfg+"-day2", "-o", "jsonpath={.metadata.name}")
		}, 3*time.Minute, 5*time.Second).Should(Equal(cfg + "-day2"))

		By("deleting the config")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "nixosconfiguration", cfg,
			"-n", orchNamespace, "--wait=false"))

		By("an orphan decommission NixJob appears with the operation label and NO ownerRef")
		Eventually(func() string {
			return kgetIn(orchNamespace, "nixjob", cfg+"-onremove", "-o", "jsonpath={.metadata.name}")
		}, 3*time.Minute, 5*time.Second).Should(Equal(cfg + "-onremove"))

		Expect(kgetIn(orchNamespace, "nixjob", cfg+"-onremove",
			"-o", "jsonpath={.metadata.labels.nio\\.homystack\\.com/operation}")).To(Equal("decommission"))
		Expect(kgetIn(orchNamespace, "nixjob", cfg+"-onremove",
			"-o", "jsonpath={.metadata.ownerReferences}")).To(BeEmpty(),
			"decommission NixJob must be an orphan (no ownerReferences)")
	})
})
