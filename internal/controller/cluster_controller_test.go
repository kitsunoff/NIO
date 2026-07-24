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
	"fmt"
	"reflect"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	niov1alpha1 "github.com/kitsunoff/nixos-operator/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// S1 / S5 — selection algorithm (pure, deterministic + stable + sticky).
// ---------------------------------------------------------------------------

func i32(v int32) *int32 { return &v }

func TestSelectGroupMembers(t *testing.T) {
	cases := []struct {
		name       string
		candidates []string // must be sorted ascending (as matchingMachines returns)
		prev       []string
		count      *int32
		want       []string
		wantUnder  bool
	}{
		{
			name:       "count unset selects all matching",
			candidates: []string{"m-01", "m-02", "m-03"},
			count:      nil,
			want:       []string{"m-01", "m-02", "m-03"},
		},
		{
			name:       "S1.3 deterministic initial: 5 machines count 3 -> 3 lowest",
			candidates: []string{"m-01", "m-02", "m-03", "m-04", "m-05"},
			count:      i32(3),
			want:       []string{"m-01", "m-02", "m-03"},
		},
		{
			name:       "S1.4 stable: identical inputs -> identical members",
			candidates: []string{"m-01", "m-02", "m-03", "m-04", "m-05"},
			prev:       []string{"m-01", "m-02", "m-03"},
			count:      i32(3),
			want:       []string{"m-01", "m-02", "m-03"},
		},
		{
			name:       "S1.5 sticky: adding a lower name does not evict an existing member",
			candidates: []string{"m-00", "m-01", "m-02", "m-03", "m-04", "m-05"},
			prev:       []string{"m-01", "m-02", "m-03"},
			count:      i32(3),
			want:       []string{"m-01", "m-02", "m-03"},
		},
		{
			name:       "S1.6 vacancy filled by next sorted candidate above current members",
			candidates: []string{"m-00", "m-01", "m-03", "m-04", "m-05"}, // m-02 deleted, m-00 present
			prev:       []string{"m-01", "m-02", "m-03"},
			count:      i32(3),
			want:       []string{"m-01", "m-03", "m-04"},
		},
		{
			name:       "S5 under-provisioned: count 3, only 2 available",
			candidates: []string{"m-01", "m-02"},
			count:      i32(3),
			want:       []string{"m-01", "m-02"},
			wantUnder:  true,
		},
		{
			name:       "over count (count reduced) drops highest-name extras",
			candidates: []string{"m-01", "m-02", "m-03"},
			prev:       []string{"m-01", "m-02", "m-03"},
			count:      i32(2),
			want:       []string{"m-01", "m-02"},
		},
		{
			name:       "vacancy fallback to lowest-first when nothing above max is free",
			candidates: []string{"m-00", "m-01", "m-08"},
			prev:       []string{"m-08"},
			count:      i32(2),
			want:       []string{"m-00", "m-08"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, under := selectGroupMembers(tc.candidates, tc.prev, tc.count)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("members = %v, want %v", got, tc.want)
			}
			if under != tc.wantUnder {
				t.Errorf("underprovisioned = %v, want %v", under, tc.wantUnder)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// S3 — node-file generation (value mapping + path/name validation).
// ---------------------------------------------------------------------------

func TestRenderMemberNodeFile_Content(t *testing.T) {
	values := &apiextensionsv1.JSON{Raw: []byte(`{"k3s":{"role":"server"}}`)}
	nf, err := renderMemberNodeFile("prod", "node-01", "10.0.0.5", values)
	if err != nil {
		t.Fatalf("renderMemberNodeFile: %v", err)
	}
	if nf.Path != "modules/nodes/node-01.nix" {
		t.Errorf("path = %q, want modules/nodes/node-01.nix", nf.Path)
	}
	if nf.Inline == nil {
		t.Fatal("expected inline content")
	}
	c := *nf.Inline

	wantSubstr := []string{
		`nixcluster."prod".members."node-01"`,
		`recursiveUpdate`,
		`builtins.fromJSON`,
		`"k3s"`, `"role"`, `"server"`, // the values JSON round-trips
		`install.ip = "10.0.0.5"`,
	}
	for _, s := range wantSubstr {
		if !strings.Contains(c, s) {
			t.Errorf("content missing %q\n---\n%s", s, c)
		}
	}
	// Members must inherit the cluster-level default: no nixosConfiguration.
	if strings.Contains(c, "nixosConfiguration") {
		t.Errorf("content must not set nixosConfiguration\n---\n%s", c)
	}
}

func TestRenderMemberNodeFile_NilValues(t *testing.T) {
	nf, err := renderMemberNodeFile("prod", "node-01", "10.0.0.5", nil)
	if err != nil {
		t.Fatalf("renderMemberNodeFile: %v", err)
	}
	if !strings.Contains(*nf.Inline, "builtins.fromJSON") {
		t.Errorf("nil values should still fromJSON an empty object\n%s", *nf.Inline)
	}
}

func TestRenderMemberNodeFile_HostileName(t *testing.T) {
	hostile := []string{
		"../../etc/passwd",
		"foo/bar",
		"..",
		"a;rm -rf /",
		"a b",
		"",
	}
	for _, name := range hostile {
		if _, err := renderMemberNodeFile("prod", name, "10.0.0.5", nil); err == nil {
			t.Errorf("renderMemberNodeFile(%q) = nil error, want rejection", name)
		}
	}
}

// ---------------------------------------------------------------------------
// S4 — converge NixCronJob shape (pure builder).
// ---------------------------------------------------------------------------

func TestDesiredConvergeCronJob_Shape(t *testing.T) {
	cluster := &niov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"},
		Spec: niov1alpha1.ClusterSpec{
			Source:    niov1alpha1.NixSource{GitRepo: "https://example.com/prod", Ref: "main"},
			SSHKeyRef: &niov1alpha1.SecretReference{Name: "cluster-ssh"},
			AgeKeyRef: &niov1alpha1.SecretReference{Name: "cluster-age"},
		},
	}
	files := []niov1alpha1.NixFile{{Path: "modules/nodes/node-01.nix", Inline: ptr("content")}}

	cron := desiredConvergeCronJob(cluster, files)

	if cron.Name != "prod-converge" {
		t.Errorf("name = %q, want prod-converge", cron.Name)
	}
	if cron.Spec.Nix.Run != ".#cluster-prod" {
		t.Errorf("run = %q, want .#cluster-prod", cron.Spec.Nix.Run)
	}
	if !reflect.DeepEqual(cron.Spec.Nix.Args, []string{"converge"}) {
		t.Errorf("args = %v, want [converge]", cron.Spec.Nix.Args)
	}
	if cron.Spec.Nix.TriggerOnChange == nil || !*cron.Spec.Nix.TriggerOnChange {
		t.Errorf("triggerOnChange must be true")
	}
	if len(cron.Spec.Nix.AdditionalFiles) != 1 {
		t.Errorf("additionalFiles = %d, want 1", len(cron.Spec.Nix.AdditionalFiles))
	}
	if cron.Spec.CronJobTemplate.Schedule != "*/30 * * * *" {
		t.Errorf("schedule = %q, want */30 * * * *", cron.Spec.CronJobTemplate.Schedule)
	}
	if cron.Spec.CronJobTemplate.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Errorf("concurrencyPolicy = %q, want Forbid", cron.Spec.CronJobTemplate.ConcurrencyPolicy)
	}
	deadline := cron.Spec.CronJobTemplate.JobTemplate.Spec.ActiveDeadlineSeconds
	if deadline == nil || *deadline != convergeActiveDeadlineSeconds {
		t.Errorf("activeDeadlineSeconds = %v, want %d", deadline, convergeActiveDeadlineSeconds)
	}

	pod := cron.Spec.CronJobTemplate.JobTemplate.Spec.Template
	assertVolume(t, pod.Spec.Volumes, clusterSSHVolumeName, "cluster-ssh")
	assertVolume(t, pod.Spec.Volumes, clusterAgeVolumeName, "cluster-age")

	var app *corev1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == defaultAppContainer {
			app = &pod.Spec.Containers[i]
		}
	}
	if app == nil {
		t.Fatalf("no %q container", defaultAppContainer)
	}
	assertMount(t, app.VolumeMounts, clusterSSHVolumeName, clusterSSHMountPath)
	assertMount(t, app.VolumeMounts, clusterAgeVolumeName, clusterAgeMountPath)
	assertEnv(t, app.Env, "NIX_SSHOPTS")
	assertEnv(t, app.Env, "SOPS_AGE_KEY_FILE")
}

func TestDesiredConvergeCronJob_CustomSchedule(t *testing.T) {
	const customSchedule = "0 * * * *"
	cluster := &niov1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"},
		Spec:       niov1alpha1.ClusterSpec{DayTwoSchedule: customSchedule},
	}
	cron := desiredConvergeCronJob(cluster, nil)
	if cron.Spec.CronJobTemplate.Schedule != customSchedule {
		t.Errorf("schedule = %q, want custom", cron.Spec.CronJobTemplate.Schedule)
	}
}

func assertVolume(t *testing.T, vols []corev1.Volume, name, secret string) {
	t.Helper()
	for _, v := range vols {
		if v.Name == name {
			if v.Secret == nil || v.Secret.SecretName != secret {
				t.Errorf("volume %q secret = %v, want %q", name, v.Secret, secret)
			}
			return
		}
	}
	t.Errorf("volume %q not mounted", name)
}

func assertMount(t *testing.T, mounts []corev1.VolumeMount, name, path string) {
	t.Helper()
	for _, m := range mounts {
		if m.Name == name {
			if m.MountPath != path {
				t.Errorf("mount %q path = %q, want %q", name, m.MountPath, path)
			}
			return
		}
	}
	t.Errorf("mount %q missing", name)
}

func assertEnv(t *testing.T, env []corev1.EnvVar, name string) {
	t.Helper()
	for _, e := range env {
		if e.Name == name {
			return
		}
	}
	t.Errorf("env %q missing", name)
}

// ---------------------------------------------------------------------------
// End-to-end reconcile (envtest) — S1 stability/stickiness, S2, S5.
// ---------------------------------------------------------------------------

var _ = Describe("Cluster Controller", func() {
	var counter int
	ctx := context.Background()

	newReconciler := func() *ClusterReconciler {
		return &ClusterReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(50),
		}
	}

	var ns string

	makeMachine := func(name string, labels map[string]string) {
		m := &niov1alpha1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
			Spec:       niov1alpha1.MachineSpec{Host: "10.0.0." + name[len(name)-1:]},
		}
		Expect(k8sClient.Create(ctx, m)).To(Succeed())
	}

	BeforeEach(func() {
		counter++
		ns = fmt.Sprintf("cluster-test-%d", counter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})).To(Succeed())
	})

	// memberNames returns the sorted member names of a named group in status.
	memberNames := func(name, group string) []string {
		var c niov1alpha1.Cluster
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &c)).To(Succeed())
		for _, g := range c.Status.NodeGroups {
			if g.Name == group {
				out := make([]string, 0, len(g.Members))
				for _, m := range g.Members {
					out = append(out, m.Name)
				}
				return out
			}
		}
		return nil
	}

	Context("S1 — deterministic, stable, sticky selection", func() {
		It("selects the lowest names, stays stable, sticky, and refills vacancies", func() {
			for _, n := range []string{"m-01", "m-02", "m-03", "m-04", "m-05"} {
				makeMachine(n, map[string]string{"role": "worker"})
			}
			name := "sel"
			cluster := &niov1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: niov1alpha1.ClusterSpec{
					Source: niov1alpha1.NixSource{GitRepo: "https://example.com/r", Ref: "main"},
					NodeGroups: []niov1alpha1.NodeGroup{{
						Name:     "workers",
						Selector: metav1.LabelSelector{MatchLabels: map[string]string{"role": "worker"}},
						Count:    i32(3),
					}},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			nn := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
			r := newReconciler()

			By("S1.3 initial selection = 3 lowest names, sorted")
			_, err := r.Reconcile(ctx, nn)
			Expect(err).NotTo(HaveOccurred())
			Expect(memberNames(name, "workers")).To(Equal([]string{"m-01", "m-02", "m-03"}))

			By("S1.4 re-reconcile is identical")
			_, err = r.Reconcile(ctx, nn)
			Expect(err).NotTo(HaveOccurred())
			Expect(memberNames(name, "workers")).To(Equal([]string{"m-01", "m-02", "m-03"}))

			By("S1.5 adding a lower name does not evict an existing member")
			makeMachine("m-00", map[string]string{"role": "worker"})
			_, err = r.Reconcile(ctx, nn)
			Expect(err).NotTo(HaveOccurred())
			Expect(memberNames(name, "workers")).To(Equal([]string{"m-01", "m-02", "m-03"}))

			By("S1.6 deleting a member refills with the next sorted candidate")
			Expect(k8sClient.Delete(ctx, &niov1alpha1.Machine{
				ObjectMeta: metav1.ObjectMeta{Name: "m-02", Namespace: ns},
			})).To(Succeed())
			_, err = r.Reconcile(ctx, nn)
			Expect(err).NotTo(HaveOccurred())
			Expect(memberNames(name, "workers")).To(Equal([]string{"m-01", "m-03", "m-04"}))
		})
	})

	Context("S2 — one nodeGroup per machine", func() {
		It("claims a matching Machine in the first group only", func() {
			makeMachine("dual", map[string]string{"role": "server", "tier": "a"})
			makeMachine("srv", map[string]string{"role": "server"})
			makeMachine("wrk", map[string]string{"tier": "a"})

			name := "claim"
			cluster := &niov1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: niov1alpha1.ClusterSpec{
					Source: niov1alpha1.NixSource{GitRepo: "https://example.com/r", Ref: "main"},
					NodeGroups: []niov1alpha1.NodeGroup{
						{Name: "servers", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"role": "server"}}},
						{Name: "tierA", Selector: metav1.LabelSelector{MatchLabels: map[string]string{"tier": "a"}}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(memberNames(name, "servers")).To(ConsistOf("dual", "srv"))
			// "dual" matched both selectors but is claimed only by the first group.
			Expect(memberNames(name, "tierA")).To(Equal([]string{"wrk"}))
		})
	})

	Context("S5 — under-provisioned", func() {
		It("surfaces an Underprovisioned condition and selects what is available", func() {
			makeMachine("m-01", map[string]string{"role": "worker"})
			makeMachine("m-02", map[string]string{"role": "worker"})

			name := "under"
			cluster := &niov1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: niov1alpha1.ClusterSpec{
					Source: niov1alpha1.NixSource{GitRepo: "https://example.com/r", Ref: "main"},
					NodeGroups: []niov1alpha1.NodeGroup{{
						Name:     "workers",
						Selector: metav1.LabelSelector{MatchLabels: map[string]string{"role": "worker"}},
						Count:    i32(3),
					}},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(memberNames(name, "workers")).To(Equal([]string{"m-01", "m-02"}))

			var c niov1alpha1.Cluster
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &c)).To(Succeed())
			cond := meta.FindStatusCondition(c.Status.Conditions, niov1alpha1.ConditionUnderprovisioned)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("S4 — converge NixCronJob is created and owned", func() {
		It("creates exactly one owned <cluster>-converge NixCronJob with the node files", func() {
			makeMachine("node-01", map[string]string{"role": "server"})

			name := "conv"
			cluster := &niov1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: niov1alpha1.ClusterSpec{
					Source:    niov1alpha1.NixSource{GitRepo: "https://example.com/r", Ref: "main"},
					SSHKeyRef: &niov1alpha1.SecretReference{Name: "cluster-ssh"},
					AgeKeyRef: &niov1alpha1.SecretReference{Name: "cluster-age"},
					NodeGroups: []niov1alpha1.NodeGroup{{
						Name:     "servers",
						Selector: metav1.LabelSelector{MatchLabels: map[string]string{"role": "server"}},
						Values:   &apiextensionsv1.JSON{Raw: []byte(`{"k3s":{"role":"server"}}`)},
					}},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
			})
			Expect(err).NotTo(HaveOccurred())

			var cron niov1alpha1.NixCronJob
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name + "-converge", Namespace: ns}, &cron)).To(Succeed())
			Expect(cron.Spec.Nix.Run).To(Equal(".#cluster-" + name))
			Expect(cron.Spec.Nix.Args).To(Equal([]string{"converge"}))
			Expect(cron.Spec.CronJobTemplate.ConcurrencyPolicy).To(Equal(batchv1.ForbidConcurrent))
			Expect(cron.Spec.Nix.AdditionalFiles).To(HaveLen(1))
			Expect(cron.Spec.Nix.AdditionalFiles[0].Path).To(Equal("modules/nodes/node-01.nix"))
			Expect(cron.OwnerReferences).To(HaveLen(1))
			Expect(cron.OwnerReferences[0].Kind).To(Equal("Cluster"))

			var c niov1alpha1.Cluster
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &c)).To(Succeed())
			Expect(c.Status.ConvergeJobRef).To(Equal(name + "-converge"))
		})
	})
})
