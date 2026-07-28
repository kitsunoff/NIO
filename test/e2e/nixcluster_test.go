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
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kitsunoff/nixos-operator/test/utils"
)

// clustersNamespace is where the NixCluster + Machine CRs are exercised. It is
// separate from nio-workloads so NixCluster selection (which lists every Machine in
// the namespace) never sees the workload fixtures.
const clustersNamespace = "nio-clusters"

// Fully-qualified kinds avoid any short-name ambiguity with other CRDs.
const (
	clusterKind = "nixclusters.nio.homystack.com"
	machineKind = "machines.nio.homystack.com"
	cronKind    = "nixcronjobs.nio.homystack.com"
)

// kcget runs `kubectl -n nio-clusters get ...` and returns trimmed stdout, for
// Eventually/Consistently polling on -o jsonpath.
//
// A failed invocation is retried once and then reported as an error string rather
// than as "". Collapsing a kubectl failure into an empty result makes a harness
// hiccup indistinguishable from "the controller wrote no members", which inside a
// Consistently fails the assertion with a diagnosis that is simply wrong.
func kcget(args ...string) string {
	full := append([]string{"get", "-n", clustersNamespace}, args...)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		out, err := utils.Run(exec.Command("kubectl", full...))
		if err == nil {
			return strings.TrimSpace(out)
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return fmt.Sprintf("<kubectl failed: %v>", lastErr)
}

// kcdelete deletes resources in nio-clusters, ignoring not-found, bounded by a
// timeout so a stuck finalizer surfaces instead of hanging the suite.
func kcdelete(args ...string) {
	full := append([]string{"delete", "-n", clustersNamespace, "--ignore-not-found", "--timeout=90s"}, args...)
	_, _ = utils.Run(exec.Command("kubectl", full...))
}

// applyMachine applies a labeled Machine. Selection depends on labels alone; the
// host need not be reachable (tier-2 never runs converge).
func applyMachine(name, host string, machineLabels map[string]string) {
	var b strings.Builder
	for k, v := range machineLabels {
		fmt.Fprintf(&b, "\n    %s: %q", k, v)
	}
	applyYAML(fmt.Sprintf(`
apiVersion: nio.homystack.com/v1alpha1
kind: Machine
metadata:
  name: %s
  namespace: %s
  labels:%s
spec:
  host: %q
`, name, clustersNamespace, b.String(), host))
}

// groupMembers returns the space-separated member names for a nodeGroup, in the
// controller's stable sorted order.
func groupMembers(cluster, group string) string {
	return kcget(clusterKind, cluster, "-o",
		fmt.Sprintf("jsonpath={.status.nodeGroups[?(@.name=='%s')].members[*].name}", group))
}

// convergeNodeFile returns the inline content of a rendered node file on the
// owned converge NixCronJob (the additionalFiles entry for <machine>.nix).
func convergeNodeFile(cluster, machine string) string {
	return kcget(cronKind, cluster+"-converge", "-o",
		fmt.Sprintf("jsonpath={.spec.nix.additionalFiles[?(@.path=='modules/nodes/%s.nix')].inline}", machine))
}

var _ = Describe("NixCluster (tier-2)", Ordered, func() {
	// clusterRev pins the converge NixCronJob source. Its content is irrelevant
	// for tier-2 (converge never runs on Kind); pinning a resolved SHA on the host
	// sidesteps in-cluster git ls-remote (the operator image is distroless).
	var clusterRev string

	// clusterSource renders the pinned source block shared by every NixCluster.
	clusterSource := func() string {
		return `{gitRepo: "https://github.com/kitsunoff/NIO", rev: "` + clusterRev + `"}`
	}

	BeforeAll(func() {
		By("ensuring the controller-manager is Available")
		_, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
			"deploy/go-operator-controller-manager", "-n", namespace, "--timeout=180s"))
		Expect(err).NotTo(HaveOccurred())

		By("resolving a pinned source revision")
		out, err := utils.Run(exec.Command("git", "ls-remote",
			"https://github.com/kitsunoff/NIO.git", "refs/heads/main"))
		Expect(err).NotTo(HaveOccurred())
		clusterRev = strings.Fields(out)[0]
		Expect(clusterRev).NotTo(BeEmpty())

		By("creating the clusters namespace")
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", clustersNamespace))
	})

	AfterEach(func() {
		// Scenarios are independent: clear all NixClusters (waits out the finalizer,
		// which GCs the owned converge cron), then Machines/Secrets, so no object
		// leaks into the next scenario's namespace-wide Machine listing.
		By("cleaning up cluster-scenario objects")
		kcdelete(clusterKind, "--all")
		kcdelete(cronKind, "--all")
		kcdelete(machineKind, "--all")
		kcdelete("secret", "--all")
	})

	AfterAll(func() {
		By("removing the clusters namespace")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", clustersNamespace, "--wait=false"))
	})

	// S1 — deterministic + stable + sticky selection.
	It("selects a deterministic, stable, sticky subset (S1)", func() {
		By("creating 5 worker Machines m-01..m-05")
		for _, n := range []string{"m-01", "m-02", "m-03", "m-04", "m-05"} {
			applyMachine(n, "10.0.0."+strings.TrimPrefix(n, "m-0"), map[string]string{"role": "worker"})
		}

		By("applying a NixCluster with a workers nodeGroup, count 3")
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixCluster
metadata: {name: c-s1, namespace: ` + clustersNamespace + `}
spec:
  source: ` + clusterSource() + `
  nodeGroups:
    - name: workers
      selector: {matchLabels: {role: worker}}
      count: 3`)

		By("the 3 lowest names are selected, in sorted order")
		Eventually(func() string {
			return groupMembers("c-s1", "workers")
		}, 2*time.Minute, 3*time.Second).Should(Equal("m-01 m-02 m-03"))

		By("re-touching the NixCluster yields an identical member list (stable)")
		_, _ = utils.Run(exec.Command("kubectl", "-n", clustersNamespace, "annotate",
			clusterKind, "c-s1", "e2e.nio/touch=1", "--overwrite"))
		Consistently(func() string {
			return groupMembers("c-s1", "workers")
		}, 15*time.Second, 3*time.Second).Should(Equal("m-01 m-02 m-03"))

		By("adding a lower-sorting Machine m-00 does not evict an existing member (sticky)")
		applyMachine("m-00", "10.0.0.100", map[string]string{"role": "worker"})
		Consistently(func() string {
			return groupMembers("c-s1", "workers")
		}, 15*time.Second, 3*time.Second).Should(Equal("m-01 m-02 m-03"))

		By("deleting a member (m-02) tops up with the next sorted candidate (m-04)")
		kcdelete(machineKind, "m-02")
		Eventually(func() string {
			return groupMembers("c-s1", "workers")
		}, 2*time.Minute, 3*time.Second).Should(Equal("m-01 m-03 m-04"))
	})

	// S2 — a Machine belongs to strictly one nodeGroup (first match wins).
	It("assigns each Machine to only the first matching nodeGroup (S2)", func() {
		By("creating a dual-labeled Machine and a second-only Machine")
		applyMachine("dual", "10.0.1.1", map[string]string{"role": "server", "zone": "z1"})
		applyMachine("only-second", "10.0.1.2", map[string]string{"zone": "z1"})

		By("applying a NixCluster whose two nodeGroups both match 'dual'")
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixCluster
metadata: {name: c-s2, namespace: ` + clustersNamespace + `}
spec:
  source: ` + clusterSource() + `
  nodeGroups:
    - name: first
      selector: {matchLabels: {role: server}}
    - name: second
      selector: {matchLabels: {zone: z1}}`)

		By("'dual' appears in the first group only")
		Eventually(func() string {
			return groupMembers("c-s2", "first")
		}, 2*time.Minute, 3*time.Second).Should(Equal("dual"))

		By("the second group excludes 'dual' and keeps only-second")
		Eventually(func() string {
			return groupMembers("c-s2", "second")
		}, 2*time.Minute, 3*time.Second).Should(Equal("only-second"))
		Expect(groupMembers("c-s2", "second")).NotTo(ContainSubstring("dual"))
	})

	// S3 — node-file value mapping (escaped, double-quoted Nix string).
	It("renders a per-member node file mapping values + host (S3)", func() {
		By("creating a selected Machine node-01 with host 10.0.0.5")
		applyMachine("node-01", "10.0.0.5", map[string]string{"role": "cp"})

		By("applying a NixCluster whose nodeGroup carries values k3s.role=server")
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixCluster
metadata: {name: c-s3, namespace: ` + clustersNamespace + `}
spec:
  source: ` + clusterSource() + `
  nodeGroups:
    - name: servers
      selector: {matchLabels: {role: cp}}
      values:
        k3s:
          role: server`)

		By("the converge cron carries an additionalFiles entry for node-01.nix")
		Eventually(func() string {
			return convergeNodeFile("c-s3", "node-01")
		}, 2*time.Minute, 3*time.Second).Should(ContainSubstring("lib.recursiveUpdate (builtins.fromJSON \""))

		nodeFile := convergeNodeFile("c-s3", "node-01")

		By("it assigns the member via recursiveUpdate of fromJSON")
		Expect(nodeFile).To(ContainSubstring(`nixcluster."c-s3".members."node-01"`))
		Expect(nodeFile).To(ContainSubstring(`lib.recursiveUpdate (builtins.fromJSON "`))

		By("the values JSON is present as an escaped double-quoted string")
		Expect(nodeFile).To(ContainSubstring(`\"k3s\"`))
		Expect(nodeFile).To(ContainSubstring(`\"role\"`))
		Expect(nodeFile).To(ContainSubstring(`\"server\"`))

		By("the Machine host is mapped to install.ip")
		Expect(nodeFile).To(ContainSubstring(`install.ip = "10.0.0.5";`))

		By("it does NOT set nixosConfiguration (inherits the cluster default)")
		Expect(nodeFile).NotTo(ContainSubstring("nixosConfiguration"))
	})

	// S4 — converge NixCronJob shape (run/args/schedule/policy/mounts/deadline).
	It("owns exactly one converge NixCronJob with the expected shape (S4)", func() {
		By("creating dummy SSH + age Secrets and a selected Machine")
		applyYAML(`
apiVersion: v1
kind: Secret
metadata: {name: cluster-ssh, namespace: ` + clustersNamespace + `}
stringData: {ssh-privatekey: "dummy-key"}
`)
		applyYAML(`
apiVersion: v1
kind: Secret
metadata: {name: cluster-age, namespace: ` + clustersNamespace + `}
stringData: {keys.txt: "dummy-age"}
`)
		applyMachine("s4-node", "10.0.2.1", map[string]string{"role": "worker"})

		By("applying a NixCluster referencing both key Secrets")
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixCluster
metadata: {name: c-s4, namespace: ` + clustersNamespace + `}
spec:
  source: ` + clusterSource() + `
  sshKeyRef: {name: cluster-ssh}
  ageKeyRef: {name: cluster-age}
  nodeGroups:
    - name: workers
      selector: {matchLabels: {role: worker}}`)

		By("the owned converge NixCronJob exists")
		Eventually(func() string {
			return kcget(cronKind, "c-s4-converge", "-o", "jsonpath={.metadata.name}")
		}, 2*time.Minute, 3*time.Second).Should(Equal("c-s4-converge"))

		By("nix.run targets the per-cluster app and args is [converge]")
		Expect(kcget(cronKind, "c-s4-converge", "-o", "jsonpath={.spec.nix.run}")).
			To(Equal(".#cluster-c-s4"))
		Expect(kcget(cronKind, "c-s4-converge", "-o", "jsonpath={.spec.nix.args[*]}")).
			To(Equal("converge"))

		By("triggerOnChange is true")
		Expect(kcget(cronKind, "c-s4-converge", "-o", "jsonpath={.spec.nix.triggerOnChange}")).
			To(Equal("true"))

		By("the schedule defaults and concurrency is Forbid")
		Expect(kcget(cronKind, "c-s4-converge", "-o", "jsonpath={.spec.cronJobTemplate.schedule}")).
			To(Equal("*/30 * * * *"))
		Expect(kcget(cronKind, "c-s4-converge", "-o", "jsonpath={.spec.cronJobTemplate.concurrencyPolicy}")).
			To(Equal("Forbid"))

		By("activeDeadlineSeconds is set")
		Expect(kcget(cronKind, "c-s4-converge", "-o",
			"jsonpath={.spec.cronJobTemplate.jobTemplate.spec.activeDeadlineSeconds}")).
			NotTo(BeEmpty())

		By("the SSH key and age key volumes are mounted on the converge pod")
		volumes := kcget(cronKind, "c-s4-converge", "-o",
			"jsonpath={.spec.cronJobTemplate.jobTemplate.spec.template.spec.volumes[*].name}")
		Expect(volumes).To(ContainSubstring("nio-cluster-ssh"))
		Expect(volumes).To(ContainSubstring("nio-cluster-age"))

		By("the rendered node file is present as an additionalFile")
		Expect(kcget(cronKind, "c-s4-converge", "-o",
			"jsonpath={.spec.nix.additionalFiles[*].path}")).
			To(ContainSubstring("modules/nodes/s4-node.nix"))
	})

	// S5 — under-provisioned nodeGroup surfaces a condition (not silent).
	It("surfaces Underprovisioned when fewer Machines match than requested (S5)", func() {
		By("creating only 2 matching Machines for a count of 3")
		applyMachine("u-01", "10.0.3.1", map[string]string{"role": "under"})
		applyMachine("u-02", "10.0.3.2", map[string]string{"role": "under"})

		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixCluster
metadata: {name: c-s5, namespace: ` + clustersNamespace + `}
spec:
  source: ` + clusterSource() + `
  nodeGroups:
    - name: workers
      selector: {matchLabels: {role: under}}
      count: 3`)

		By("the Underprovisioned condition is True")
		Eventually(func() string {
			return kcget(clusterKind, "c-s5", "-o",
				"jsonpath={.status.conditions[?(@.type=='Underprovisioned')].status}")
		}, 2*time.Minute, 3*time.Second).Should(Equal("True"))

		By("members are the 2 available Machines")
		Expect(groupMembers("c-s5", "workers")).To(Equal("u-01 u-02"))
	})
})
