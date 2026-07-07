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
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kitsunoff/nixos-operator/test/utils"
)

// workloadsNamespace is where the Nix workload CRs are exercised.
const workloadsNamespace = "nio-workloads"

// applyYAML writes the manifest to a temp file and applies it to the cluster.
func applyYAML(manifest string) {
	f, err := os.CreateTemp("", "nio-e2e-*.yaml")
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.WriteString(manifest)
	Expect(err).NotTo(HaveOccurred())
	Expect(f.Close()).To(Succeed())
	_, err = utils.Run(exec.Command("kubectl", "apply", "-f", f.Name()))
	Expect(err).NotTo(HaveOccurred(), "failed to apply manifest:\n%s", manifest)
}

// kget runs `kubectl -n <workloadsNamespace> get ...` and returns trimmed stdout.
func kget(args ...string) string {
	full := append([]string{"get", "-n", workloadsNamespace}, args...)
	out, err := utils.Run(exec.Command("kubectl", full...))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

var _ = Describe("Nix workloads", Ordered, func() {
	// nioRev is a real, fetchable commit of a small public repo. Its content is
	// irrelevant — the workloads run external cached flakes — but fetch-source
	// must be able to shallow-fetch it. Pinning Rev also sidesteps in-cluster
	// git ls-remote (the operator image is distroless, without git).
	var nioRev string

	BeforeAll(func() {
		By("ensuring the controller-manager is Available")
		_, err := utils.Run(exec.Command("kubectl", "wait", "--for=condition=Available",
			"deploy/go-operator-controller-manager", "-n", namespace, "--timeout=180s"))
		Expect(err).NotTo(HaveOccurred())

		By("resolving a pinned source revision")
		out, err := utils.Run(exec.Command("git", "ls-remote",
			"https://github.com/kitsunoff/NIO.git", "refs/heads/main"))
		Expect(err).NotTo(HaveOccurred())
		nioRev = strings.Fields(out)[0]
		Expect(nioRev).NotTo(BeEmpty())

		By("creating the workloads namespace")
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", workloadsNamespace))

		By("creating a NixStore and waiting for it to be Ready")
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixStore
metadata: {name: store, namespace: ` + workloadsNamespace + `}
spec:
  storage: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 3Gi}}}
`)
		Eventually(func() string {
			return kget("nixstore", "store", "-o", "jsonpath={.status.phase}")
		}, 8*time.Minute, 5*time.Second).Should(Equal("Ready"), "NixStore did not reach Ready")

		By("creating a NixBuilder and waiting for it to be Ready")
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixBuilder
metadata: {name: builder, namespace: ` + workloadsNamespace + `}
spec:
  storeRef: {name: store}
`)
		Eventually(func() string {
			return kget("nixbuilder", "builder", "-o", "jsonpath={.status.ready}")
		}, 6*time.Minute, 5*time.Second).Should(Equal("true"), "NixBuilder did not become Ready")
	})

	AfterAll(func() {
		By("removing the workloads namespace")
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", workloadsNamespace, "--wait=false"))
	})

	// deployManifest builds a long-running NixDeployment/NixStatefulSet spec that
	// runs a tiny cached package (bash sleeping), so the pod substitutes and runs
	// without any real build.
	longRunningNix := func() string {
		return `
    source: {gitRepo: "https://github.com/kitsunoff/NIO", rev: "` + nioRev + `"}
    run: "nixpkgs#bash"
    args: ["-c", "sleep 3600"]
    storeRef: {name: store}`
	}

	It("rolls a NixDeployment to Ready with pods that substitute and run", func() {
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixDeployment
metadata: {name: web, namespace: ` + workloadsNamespace + `}
spec:
  nix:` + longRunningNix() + `
  deploymentTemplate:
    replicas: 1`)

		Eventually(func() string {
			return kget("nixdeployment", "web", "-o", "jsonpath={.status.phase}")
		}, 8*time.Minute, 5*time.Second).Should(Equal("Ready"), "NixDeployment did not reach Ready")

		Expect(kget("deploy", "web", "-o", "jsonpath={.status.availableReplicas}")).To(Equal("1"))
	})

	It("runs a NixJob to completion", func() {
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixJob
metadata: {name: greet, namespace: ` + workloadsNamespace + `}
spec:
  nix:
    source: {gitRepo: "https://github.com/kitsunoff/NIO", rev: "` + nioRev + `"}
    run: "nixpkgs#hello"
    storeRef: {name: store}`)

		Eventually(func() string {
			return kget("nixjob", "greet", "-o", "jsonpath={.status.phase}")
		}, 8*time.Minute, 5*time.Second).Should(Equal("Ready"), "NixJob did not complete")

		Expect(kget("nixjob", "greet", "-o", "jsonpath={.status.succeeded}")).To(Equal("1"))
	})

	It("fires an immediate Job for a NixCronJob on a new revision", func() {
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixCronJob
metadata: {name: tick, namespace: ` + workloadsNamespace + `}
spec:
  nix:
    source: {gitRepo: "https://github.com/kitsunoff/NIO", rev: "` + nioRev + `"}
    run: "nixpkgs#hello"
    triggerOnChange: true
    storeRef: {name: store}
  cronJobTemplate:
    schedule: "*/5 * * * *"`)

		By("the owned CronJob is created")
		Eventually(func() string {
			return kget("cronjob", "tick", "-o", "jsonpath={.spec.schedule}")
		}, 3*time.Minute, 5*time.Second).Should(Equal("*/5 * * * *"))

		By("an immediate Job is created for the workload")
		Eventually(func() int {
			out := kget("jobs", "-l", "nio.homystack.com/workload-name=tick",
				"-o", "jsonpath={.items[*].metadata.name}")
			if out == "" {
				return 0
			}
			return len(strings.Fields(out))
		}, 3*time.Minute, 5*time.Second).Should(BeNumerically(">=", 1), "no immediate Job was created")
	})

	It("rolls a NixStatefulSet to Ready", func() {
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixStatefulSet
metadata: {name: stateful, namespace: ` + workloadsNamespace + `}
spec:
  nix:` + longRunningNix() + `
  statefulSetTemplate:
    serviceName: stateful
    replicas: 1`)

		Eventually(func() string {
			return kget("nixstatefulset", "stateful", "-o", "jsonpath={.status.phase}")
		}, 8*time.Minute, 5*time.Second).Should(Equal("Ready"), "NixStatefulSet did not reach Ready")
	})

	It("stalls a NixDeployment whose revision fails to build", func() {
		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixDeployment
metadata: {name: broken, namespace: ` + workloadsNamespace + `}
spec:
  nix:
    source: {gitRepo: "https://github.com/kitsunoff/NIO", rev: "` + nioRev + `"}
    run: ".#doesnotexist"
    storeRef: {name: store}
  deploymentTemplate:
    replicas: 1`)

		By("the new-revision pod fails its instantiate build and the rollout stalls")
		Eventually(func() string {
			return kget("nixdeployment", "broken", "-o", "jsonpath={.status.phase}")
		}, 8*time.Minute, 5*time.Second).Should(Equal("Degraded"), "broken NixDeployment did not go Degraded")

		stalled := kget("nixdeployment", "broken",
			"-o", "jsonpath={.status.conditions[?(@.type=='Stalled')].status}")
		Expect(stalled).To(Equal("True"))
	})

	It("delegates a non-cached build to the NixBuilder and realizes it into the NixStore", func() {
		// A tiny public flake whose derivation embeds a unique marker, so its
		// output is in no public cache and a real build must run on the builder.
		const flakeRepo = "https://github.com/kitsunoff/nio-e2e-flake"
		const flakeRev = "882334ddb6f76fa1d7aeb839835a3c06c18c4e76"

		applyYAML(`
apiVersion: nio.homystack.com/v1alpha1
kind: NixJob
metadata: {name: rbjob, namespace: ` + workloadsNamespace + `}
spec:
  nix:
    source: {gitRepo: "` + flakeRepo + `", rev: "` + flakeRev + `"}
    run: ".#default"
    storeRef: {name: store}
    builderRef: {name: builder}`)

		By("the builder-backed NixJob completes (build ran on the builder)")
		Eventually(func() string {
			return kget("nixjob", "rbjob", "-o", "jsonpath={.status.phase}")
		}, 12*time.Minute, 10*time.Second).Should(Equal("Ready"), "builder-backed NixJob did not complete")

		By("the built path was realized into the shared NixStore")
		Eventually(func() string {
			out, err := utils.Run(exec.Command("kubectl", "exec", "-n", workloadsNamespace, "store-0",
				"-c", "store", "--", "sh", "-c", "ls -d /nix/store/*nio-e2e-app 2>/dev/null || true"))
			if err != nil {
				return ""
			}
			return strings.TrimSpace(out)
		}, 3*time.Minute, 5*time.Second).Should(ContainSubstring("nio-e2e-app"),
			"the delegated build was not pushed into the NixStore")
	})
})
