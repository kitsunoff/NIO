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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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
		// The values JSON is embedded in a double-quoted Nix string, so its own
		// quotes are backslash-escaped.
		`\"k3s\"`, `\"role\"`, `\"server\"`,
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

// TestRenderMemberNodeFile_Escaping is the P4/P5 regression guard: hostile
// values and a hostile host must never produce an unescaped antiquotation
// (${), a string-breakout ("), or the ”-boundary of the old indented-string
// renderer. Every ${ in the output MUST be preceded by a backslash.
func TestRenderMemberNodeFile_Escaping(t *testing.T) {
	cases := []struct {
		name   string
		values string
		host   string
	}{
		{
			name:   "antiquotation in a JSON value",
			values: `{"script":"echo '${VAR}'"}`,
			host:   "10.0.0.5",
		},
		{
			name:   "getEnv injection payload in values",
			values: `{"x":"'${builtins.getEnv \"HOME\"}"}`,
			host:   "10.0.0.5",
		},
		{
			name:   "backslash, quote and single quotes in values",
			values: `{"a":"b\\c\"d''e"}`,
			host:   "10.0.0.5",
		},
		{
			name:   "antiquotation injection via host",
			values: `{}`,
			host:   `10.0.0.${builtins.getEnv "HOME"}`,
		},
		{
			name:   "backslash and quote in host",
			values: `{}`,
			host:   `10.0.0.5\"x`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := &apiextensionsv1.JSON{Raw: []byte(tc.values)}
			nf, err := renderMemberNodeFile("prod", "node-01", tc.host, values)
			if err != nil {
				t.Fatalf("renderMemberNodeFile: %v", err)
			}
			c := *nf.Inline

			// Every ${ in the output must be escaped (preceded by a backslash),
			// so no live antiquotation survives into the generated Nix.
			assertEveryAntiquotationEscaped(t, c)

			// The old indented-string renderer produced ''...''; the double-quoted
			// renderer must never emit a triple-single-quote boundary.
			if strings.Contains(c, "'''") {
				t.Errorf("content contains ''' (stale indented-string boundary)\n---\n%s", c)
			}
		})
	}
}

// assertEveryAntiquotationEscaped fails if any "${" in s is not immediately
// preceded by a backslash. It only inspects the two interpolated string
// literals (values JSON + host); the surrounding template is static and Nix
// safe, so a scan of the whole rendered file is sufficient and strict.
func assertEveryAntiquotationEscaped(t *testing.T, s string) {
	t.Helper()
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '$' && s[i+1] == '{' {
			if i == 0 || s[i-1] != '\\' {
				t.Errorf("unescaped antiquotation ${ at offset %d\n---\n%s", i, s)
			}
		}
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
	cluster := &niov1alpha1.NixCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"},
		Spec: niov1alpha1.NixClusterSpec{
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
	cluster := &niov1alpha1.NixCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"},
		Spec:       niov1alpha1.NixClusterSpec{DayTwoSchedule: customSchedule},
	}
	cron := desiredConvergeCronJob(cluster, nil)
	if cron.Spec.CronJobTemplate.Schedule != customSchedule {
		t.Errorf("schedule = %q, want custom", cron.Spec.CronJobTemplate.Schedule)
	}
}

// A cluster that references a NixStore/NixBuilder must pass those through to the
// converge NixCronJob's NixSpec, so the converge pod builds against the shared
// store instead of rebuilding the whole closure in an ephemeral in-pod /nix.
func TestDesiredConvergeCronJob_StoreBuilderRef(t *testing.T) {
	cluster := &niov1alpha1.NixCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"},
		Spec: niov1alpha1.NixClusterSpec{
			Source:     niov1alpha1.NixSource{GitRepo: "https://example.com/prod", Ref: "main"},
			StoreRef:   &niov1alpha1.LocalObjectReference{Name: "cache-store"},
			BuilderRef: &niov1alpha1.LocalObjectReference{Name: "builder"},
		},
	}
	cron := desiredConvergeCronJob(cluster, nil)
	if cron.Spec.Nix.StoreRef == nil || cron.Spec.Nix.StoreRef.Name != "cache-store" {
		t.Errorf("converge NixSpec.StoreRef = %v, want {cache-store}", cron.Spec.Nix.StoreRef)
	}
	if cron.Spec.Nix.BuilderRef == nil || cron.Spec.Nix.BuilderRef.Name != "builder" {
		t.Errorf("converge NixSpec.BuilderRef = %v, want {builder}", cron.Spec.Nix.BuilderRef)
	}
}

// With neither set, the converge NixSpec leaves them nil (in-pod ephemeral build
// is the default — no store/builder acceleration required).
func TestDesiredConvergeCronJob_NoStoreBuilderByDefault(t *testing.T) {
	cluster := &niov1alpha1.NixCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"},
		Spec:       niov1alpha1.NixClusterSpec{Source: niov1alpha1.NixSource{GitRepo: "https://example.com/prod"}},
	}
	cron := desiredConvergeCronJob(cluster, nil)
	if cron.Spec.Nix.StoreRef != nil || cron.Spec.Nix.BuilderRef != nil {
		t.Errorf("expected no store/builder by default, got store=%v builder=%v",
			cron.Spec.Nix.StoreRef, cron.Spec.Nix.BuilderRef)
	}
}

// stalledConvergeCron returns a converge cron that is Degraded because its
// storeRef/builderRef could not be resolved — the state a typo in either field
// produces. Nothing was applied to any member in this state.
func stalledConvergeCron(msg string) *niov1alpha1.NixCronJob {
	cron := &niov1alpha1.NixCronJob{}
	cron.Status.Phase = niov1alpha1.PhaseDegraded
	meta.SetStatusCondition(&cron.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionStalled, Status: metav1.ConditionTrue,
		Reason: reasonInfraNotReady, Message: msg,
	})
	return cron
}

// A converge stalled on a missing NixStore/NixBuilder never touched a node, so
// reporting members as Failed would claim an apply broke them. The member keeps
// exactly the status it last reported — Pending when it never reported one.
func TestCoarseMemberStatus_InfraStallKeepsPreviousStatus(t *testing.T) {
	cron := stalledConvergeCron(`NixStore "cache-store" not found`)

	if got := coarseMemberStatus(cron, true, ""); got != niov1alpha1.MemberStatusPending {
		t.Errorf("member status with no history = %q, want %q", got, niov1alpha1.MemberStatusPending)
	}
	if got := coarseMemberStatus(cron, true, niov1alpha1.MemberStatusApplied); got != niov1alpha1.MemberStatusApplied {
		t.Errorf("previously-applied member = %q, want %q", got, niov1alpha1.MemberStatusApplied)
	}
	// The important case: a member that FAILED on the last real run must not be
	// upgraded to Applied just because a converge later stalled before running.
	now := metav1.Now()
	cron.Status.LastSuccessfulTime = &now
	if got := coarseMemberStatus(cron, true, niov1alpha1.MemberStatusFailed); got != niov1alpha1.MemberStatusFailed {
		t.Errorf("previously-failed member = %q, want %q", got, niov1alpha1.MemberStatusFailed)
	}
}

// A git error also stalls the child, but it is a different failure with a
// different meaning: it must not be mirrored as an infra stall, or the mirrored
// condition can never be cleared again (only InfraNotReady is recognised there).
func TestConvergeStall_OnlyMatchesInfraNotReady(t *testing.T) {
	cron := &niov1alpha1.NixCronJob{}
	cron.Status.Phase = niov1alpha1.PhaseDegraded
	meta.SetStatusCondition(&cron.Status.Conditions, metav1.Condition{
		Type: niov1alpha1.ConditionStalled, Status: metav1.ConditionTrue,
		Reason: reasonGitError, Message: "revision not found",
	})

	if convergeStall(cron, true) != nil {
		t.Error("a git-error stall must not be treated as an infra stall")
	}
	if got := clusterPhase(cron, true, 3); got != niov1alpha1.NixClusterPhaseDegraded {
		t.Errorf("phase on a git-error stall = %q, want %q", got, niov1alpha1.NixClusterPhaseDegraded)
	}
	if got := coarseMemberStatus(cron, true, niov1alpha1.MemberStatusApplied); got != niov1alpha1.MemberStatusFailed {
		t.Errorf("member status on a git-error stall = %q, want %q", got, niov1alpha1.MemberStatusFailed)
	}
}

// A CronJob keeps scheduling whether its runs succeed or not, so a converge that
// fails every time must still surface as a failure rather than as Ready.
func TestCoarseMemberStatus_FailedRunIsFailed(t *testing.T) {
	failedAt := metav1.NewTime(metav1.Now().Add(-time.Minute))
	cron := &niov1alpha1.NixCronJob{}
	cron.Status.Phase = niov1alpha1.PhaseDegraded
	cron.Status.LastFailedTime = &failedAt

	if got := coarseMemberStatus(cron, true, niov1alpha1.MemberStatusApplied); got != niov1alpha1.MemberStatusFailed {
		t.Errorf("member status = %q, want %q", got, niov1alpha1.MemberStatusFailed)
	}
	if got := clusterPhase(cron, true, 1); got != niov1alpha1.NixClusterPhaseDegraded {
		t.Errorf("phase = %q, want %q", got, niov1alpha1.NixClusterPhaseDegraded)
	}

	// A success AFTER the failure means the cron recovered.
	recovered := metav1.Now()
	cron.Status.LastSuccessfulTime = &recovered
	cron.Status.Phase = niov1alpha1.PhaseReady
	if got := coarseMemberStatus(cron, true, ""); got != niov1alpha1.MemberStatusApplied {
		t.Errorf("member status after recovery = %q, want %q", got, niov1alpha1.MemberStatusApplied)
	}
}

// The cluster phase and the member status must never disagree. A child can sit in
// Progressing or Suspended while its last finished run was a failure (observe()
// bails out with Progressing whenever the projected CronJob cannot be read), and
// reading only the phase there reported "cluster converged" with every member
// Failed.
func TestClusterPhaseAgreesWithMemberStatusOnAFailedRun(t *testing.T) {
	failedAt := metav1.Now()
	succeededAt := metav1.NewTime(failedAt.Add(-time.Hour))

	for _, phase := range []string{
		niov1alpha1.PhaseProgressing,
		niov1alpha1.PhaseSuspended,
		niov1alpha1.PhaseReady,
	} {
		t.Run(phase, func(t *testing.T) {
			cron := &niov1alpha1.NixCronJob{}
			cron.Status.Phase = phase
			cron.Status.LastFailedTime = &failedAt
			// Monotonic by design, so an older success survives the failure.
			cron.Status.LastSuccessfulTime = &succeededAt

			gotPhase := clusterPhase(cron, true, 3)
			gotMember := coarseMemberStatus(cron, true, niov1alpha1.MemberStatusApplied)

			if gotPhase != niov1alpha1.NixClusterPhaseDegraded {
				t.Errorf("cluster phase = %q, want %q", gotPhase, niov1alpha1.NixClusterPhaseDegraded)
			}
			if gotMember != niov1alpha1.MemberStatusFailed {
				t.Errorf("member status = %q, want %q", gotMember, niov1alpha1.MemberStatusFailed)
			}
		})
	}
}

// A builder mismatch usually becomes provable AFTER the converge cron exists:
// hardware facts arrive on a later discovery pass, and members get added to
// long-running clusters. Refusing to update the cron is not enough — the existing
// one keeps firing converges that cannot succeed.
func TestSuspendConvergeCronJob(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	cluster := &niov1alpha1.NixCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"},
	}
	cron := &niov1alpha1.NixCronJob{
		ObjectMeta: metav1.ObjectMeta{Name: convergeCronName("prod"), Namespace: "infra"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, cron).Build()
	r := &NixClusterReconciler{Client: c, Scheme: scheme}

	if err := r.suspendConvergeCronJob(context.Background(), cluster); err != nil {
		t.Fatalf("suspendConvergeCronJob: %v", err)
	}
	var got niov1alpha1.NixCronJob
	if err := c.Get(context.Background(),
		types.NamespacedName{Name: convergeCronName("prod"), Namespace: "infra"}, &got); err != nil {
		t.Fatalf("get cron: %v", err)
	}
	if !got.Spec.Nix.Suspend {
		t.Error("the converge cron must be suspended while the cluster is provably misconfigured")
	}

	// Idempotent, and a missing cron is not an error (nothing to stop yet).
	if err := r.suspendConvergeCronJob(context.Background(), cluster); err != nil {
		t.Errorf("suspending twice must be a no-op, got %v", err)
	}
	absent := &niov1alpha1.NixCluster{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "infra"}}
	if err := r.suspendConvergeCronJob(context.Background(), absent); err != nil {
		t.Errorf("no cron yet must not be an error, got %v", err)
	}
}

// latestRunFailed is the shared "is the most recent finished run a failure?"
// predicate; both the cron's own phase and the cluster's member status use it.
func TestLatestRunFailed(t *testing.T) {
	early := metav1.NewTime(metav1.Now().Add(-time.Hour))
	late := metav1.Now()

	for _, tc := range []struct {
		name            string
		failed, success *metav1.Time
		wantFailing     bool
	}{
		{name: "never ran", wantFailing: false},
		{name: "only failures", failed: &late, wantFailing: true},
		{name: "failure then success", failed: &early, success: &late, wantFailing: false},
		{name: "success then failure", failed: &late, success: &early, wantFailing: true},
		{name: "only successes", success: &late, wantFailing: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := latestRunFailed(tc.failed, tc.success)
			if got != tc.wantFailing {
				t.Errorf("latestRunFailed = %v, want %v", got, tc.wantFailing)
			}
			if got && msg == "" {
				t.Error("a failing verdict must carry an explanatory message")
			}
		})
	}
}

// A cluster waiting on a dependency it cannot resolve is Blocked, not Degraded:
// Degraded means the cluster itself is broken.
func TestClusterPhase_InfraStallIsBlocked(t *testing.T) {
	got := clusterPhase(stalledConvergeCron(`NixBuilder "builder" not ready`), true, 3)
	if got != niov1alpha1.NixClusterPhaseBlocked {
		t.Errorf("phase = %q, want %q", got, niov1alpha1.NixClusterPhaseBlocked)
	}
}

// The only accurate diagnosis lives on the child. Surface it on the NixCluster,
// so `kubectl describe nixcluster` says which reference is wrong instead of
// "converge in progress".
func TestSetConditions_PropagatesInfraStallMessage(t *testing.T) {
	const msg = `NixStore "cache-store" not found`
	cluster := &niov1alpha1.NixCluster{ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"}}
	cron := stalledConvergeCron(msg)
	cluster.Status.Phase = clusterPhase(cron, true, 2)

	(&NixClusterReconciler{}).setConditions(cluster, cron, true, false, 2)

	stalled := meta.FindStatusCondition(cluster.Status.Conditions, niov1alpha1.ConditionStalled)
	if stalled == nil || stalled.Status != metav1.ConditionTrue {
		t.Fatalf("Stalled condition = %+v, want True", stalled)
	}
	if stalled.Reason != reasonInfraNotReady || !strings.Contains(stalled.Message, msg) {
		t.Errorf("Stalled condition must carry the child's reason and message, got %+v", stalled)
	}
	ready := meta.FindStatusCondition(cluster.Status.Conditions, niov1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("Ready condition = %+v, want False", ready)
	}
	if !strings.Contains(ready.Message, msg) {
		t.Errorf("Ready message must explain the stall, got %q", ready.Message)
	}
}

// setConditions only runs once a reconcile got all the way through selection,
// rendering, the builder preflight and the converge child. Every Stalled reason
// is therefore obsolete by then — including the ones the reconciler sets itself,
// which would otherwise linger with a false message until the cluster happened
// to reach Ready.
func TestSetConditions_ClearsAnyStalledOnceTheReconcileSucceeds(t *testing.T) {
	for _, reason := range []string{
		reasonInfraNotReady,
		reasonBuilderSystemMismatch,
		niov1alpha1.ReasonInvalidNodeFile,
		niov1alpha1.ReasonSelectionComplete,
	} {
		t.Run(reason, func(t *testing.T) {
			cluster := &niov1alpha1.NixCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"},
			}
			meta.SetStatusCondition(&cluster.Status.Conditions, metav1.Condition{
				Type: niov1alpha1.ConditionStalled, Status: metav1.ConditionTrue,
				Reason: reason, Message: "a diagnosis that is now out of date",
			})

			healthy := &niov1alpha1.NixCronJob{}
			healthy.Status.Phase = niov1alpha1.PhaseProgressing
			cluster.Status.Phase = clusterPhase(healthy, true, 2)

			(&NixClusterReconciler{}).setConditions(cluster, healthy, true, false, 2)

			if cond := meta.FindStatusCondition(cluster.Status.Conditions, niov1alpha1.ConditionStalled); cond != nil {
				t.Errorf("Stalled(%s) must be cleared once the reconcile succeeds, got %+v", reason, cond)
			}
		})
	}
}

// nixSystemForArch must recognise exactly the architectures NIO supports and
// stay silent about everything else — the preflight built on it may only fire on
// a provable mismatch.
func TestNixSystemForArch(t *testing.T) {
	for in, want := range map[string]string{
		"x86_64":  "x86_64-linux",
		"amd64":   "x86_64-linux",
		"AArch64": "aarch64-linux",
		"arm64":   "aarch64-linux",
		" x86_64": "x86_64-linux",
		"riscv64": "",
		"":        "",
	} {
		if got := nixSystemForArch(in); got != want {
			t.Errorf("nixSystemForArch(%q) = %q, want %q", in, got, want)
		}
	}
}

// machineWithArch returns a Machine reporting the given architecture (empty means
// hardware facts were never collected).
func machineWithArch(arch string) *niov1alpha1.Machine {
	m := &niov1alpha1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "node-01", Namespace: "infra"}}
	if arch != "" {
		m.Status.HardwareFacts = &niov1alpha1.HardwareFacts{Architecture: arch}
	}
	return m
}

// The preflight refuses a cluster whose builder cannot build a member's arch —
// converge would otherwise burn its whole activeDeadline and fail on an opaque
// nix error, because delegation leaves no local fallback.
func TestCheckBuilderCoversMembers(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := niov1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	builder := func(name string, systems ...string) *niov1alpha1.NixBuilder {
		return &niov1alpha1.NixBuilder{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "infra"},
			Spec:       niov1alpha1.NixBuilderSpec{Systems: systems},
		}
	}
	groups := []niov1alpha1.NodeGroupStatus{{
		Name:    "control-plane",
		Members: []niov1alpha1.MemberStatus{{Name: "node-01"}},
	}}

	tests := []struct {
		name       string
		builderRef string
		objects    []client.Object
		machine    *niov1alpha1.Machine
		wantErr    bool
	}{{
		name:       "provable mismatch is refused",
		builderRef: "builder",
		objects:    []client.Object{builder("builder", "x86_64-linux")},
		machine:    machineWithArch("aarch64"),
		wantErr:    true,
	}, {
		name:       "builder covers the member",
		builderRef: "builder",
		objects:    []client.Object{builder("builder", "x86_64-linux", "aarch64-linux")},
		machine:    machineWithArch("aarch64"),
	}, {
		name:       "unqualified builder proves nothing",
		builderRef: "builder",
		objects:    []client.Object{builder("builder")},
		machine:    machineWithArch("aarch64"),
	}, {
		name:       "machine has not reported its architecture",
		builderRef: "builder",
		objects:    []client.Object{builder("builder", "x86_64-linux")},
		machine:    machineWithArch(""),
	}, {
		name:       "builder does not exist yet (the child stalls instead)",
		builderRef: "builder",
		machine:    machineWithArch("aarch64"),
	}, {
		name:    "no builderRef at all",
		machine: machineWithArch("aarch64"),
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cluster := &niov1alpha1.NixCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"},
			}
			if tc.builderRef != "" {
				cluster.Spec.BuilderRef = &niov1alpha1.LocalObjectReference{Name: tc.builderRef}
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.objects...).Build()
			r := &NixClusterReconciler{Client: c, Scheme: scheme}

			err := r.checkBuilderCoversMembers(context.Background(), cluster,
				map[string]*niov1alpha1.Machine{"node-01": tc.machine}, groups)
			if tc.wantErr && err == nil {
				t.Fatal("expected the mismatch to be refused")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "aarch64-linux") {
				t.Errorf("the error must name the system the member needs: %v", err)
			}
		})
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

var _ = Describe("NixCluster Controller", func() {
	var counter int
	ctx := context.Background()

	newReconciler := func() *NixClusterReconciler {
		return &NixClusterReconciler{
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
		var c niov1alpha1.NixCluster
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
			cluster := &niov1alpha1.NixCluster{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: niov1alpha1.NixClusterSpec{
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
			cluster := &niov1alpha1.NixCluster{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: niov1alpha1.NixClusterSpec{
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
			cluster := &niov1alpha1.NixCluster{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: niov1alpha1.NixClusterSpec{
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

			var c niov1alpha1.NixCluster
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
			cluster := &niov1alpha1.NixCluster{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
				Spec: niov1alpha1.NixClusterSpec{
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
			Expect(cron.OwnerReferences[0].Kind).To(Equal("NixCluster"))

			var c niov1alpha1.NixCluster
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &c)).To(Succeed())
			Expect(c.Status.ConvergeJobRef).To(Equal(name + "-converge"))
		})
	})
})

// ---------------------------------------------------------------------------
// Teardown on deletion — pure helpers.
// ---------------------------------------------------------------------------

func TestParseClusterResult(t *testing.T) {
	const marked = `::nixcluster:result:: {"cluster":"prod","result":"partial",` +
		`"members":[{"name":"node-01","ip":"10.0.0.1","action":"destroyed","status":"Applied","message":"","durationSeconds":3},` +
		`{"name":"node-02","ip":"10.0.0.2","action":"destroyed","status":"Skipped","message":"unreachable","durationSeconds":0}],` +
		`"steps":[{"name":"k3s.drain","phase":"workloads","status":"ok","message":""}]}`

	cases := []struct {
		name       string
		log        string
		wantOK     bool
		wantResult string
		wantNames  []string
	}{
		{
			name:       "marker line among merged stderr",
			log:        "building...\n[step] k3s.drain\n" + marked + "\ncleanup done\n",
			wantOK:     true,
			wantResult: "partial",
			wantNames:  []string{"node-01", "node-02"},
		},
		{
			name: "bare --json object is accepted as a fallback",
			log: "copying path...\n" +
				`{"cluster":"prod","result":"success","members":[{"name":"node-01","status":"Applied"}],"steps":[]}` + "\n",
			wantOK:     true,
			wantResult: "success",
			wantNames:  []string{"node-01"},
		},
		{
			name: "the last result line wins",
			log: `::nixcluster:result:: {"cluster":"prod","result":"failed","members":[],"steps":[]}` + "\n" +
				`::nixcluster:result:: {"cluster":"prod","result":"success","members":[],"steps":[]}` + "\n",
			wantOK:     true,
			wantResult: "success",
		},
		{
			name: "unrelated JSON lines are not a result",
			log:  `{"level":"info","msg":"hello"}` + "\n",
		},
		{
			name: "garbage after the marker is not a result",
			log:  "::nixcluster:result:: {not json\n",
		},
		{
			name: "empty log",
			log:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseClusterResult(tc.log)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.Result != tc.wantResult {
				t.Errorf("result = %q, want %q", got.Result, tc.wantResult)
			}
			names := make([]string, 0, len(got.Members))
			for _, m := range got.Members {
				names = append(names, m.Name)
			}
			if tc.wantNames != nil && !reflect.DeepEqual(names, tc.wantNames) {
				t.Errorf("members = %v, want %v", names, tc.wantNames)
			}
		})
	}
}

func TestTeardownBackoff(t *testing.T) {
	cases := []struct {
		failed int32
		want   time.Duration
	}{
		{0, teardownBackoffBase},
		{1, teardownBackoffBase},
		{2, 2 * teardownBackoffBase},
		{3, 4 * teardownBackoffBase},
		{10, teardownBackoffMax},
		{100, teardownBackoffMax},
	}
	for _, tc := range cases {
		if got := teardownBackoff(tc.failed); got != tc.want {
			t.Errorf("teardownBackoff(%d) = %v, want %v", tc.failed, got, tc.want)
		}
	}
}

func TestDesiredTeardownJob_CopiesTheConvergeChild(t *testing.T) {
	cluster := &niov1alpha1.NixCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "infra"},
		Spec: niov1alpha1.NixClusterSpec{
			Source:     niov1alpha1.NixSource{GitRepo: "https://example.com/prod", Ref: "main"},
			SSHKeyRef:  &niov1alpha1.SecretReference{Name: "cluster-ssh"},
			StoreRef:   &niov1alpha1.LocalObjectReference{Name: "store"},
			BuilderRef: &niov1alpha1.LocalObjectReference{Name: "builder"},
		},
	}
	files := []niov1alpha1.NixFile{{Path: "modules/nodes/node-01.nix", Inline: ptr("content")}}
	cron := desiredConvergeCronJob(cluster, files)
	// The teardown suspends the cron before copying it; neither must leak.
	cron.Spec.Nix.Suspend = true
	cron.Spec.CronJobTemplate.Suspend = ptr(true)

	job := desiredTeardownJob(cluster, cron)

	if job.Name != "prod-teardown" || job.Namespace != "infra" {
		t.Errorf("name = %s/%s, want infra/prod-teardown", job.Namespace, job.Name)
	}
	if job.Spec.Nix.Run != ".#cluster-prod" {
		t.Errorf("run = %q, want .#cluster-prod", job.Spec.Nix.Run)
	}
	if !reflect.DeepEqual(job.Spec.Nix.Args, []string{"teardown", "--auto-approve"}) {
		t.Errorf("args = %v, want [teardown --auto-approve]", job.Spec.Nix.Args)
	}
	if job.Spec.Nix.Suspend {
		t.Error("the teardown job must not inherit the cron's suspension")
	}
	if job.Spec.Nix.TriggerOnChange == nil || *job.Spec.Nix.TriggerOnChange {
		t.Error("the teardown job must run once (triggerOnChange false)")
	}
	if !reflect.DeepEqual(job.Spec.Nix.AdditionalFiles, files) {
		t.Errorf("additionalFiles = %v, want the converge node files", job.Spec.Nix.AdditionalFiles)
	}
	if job.Spec.Nix.StoreRef == nil || job.Spec.Nix.StoreRef.Name != "store" ||
		job.Spec.Nix.BuilderRef == nil || job.Spec.Nix.BuilderRef.Name != "builder" {
		t.Error("store and builder references must be carried over")
	}
	if job.Spec.JobTemplate == nil {
		t.Fatal("jobTemplate must be set")
	}
	if job.Spec.JobTemplate.BackoffLimit == nil || *job.Spec.JobTemplate.BackoffLimit != 0 {
		t.Error("backoffLimit must be 0: the controller owns retries")
	}
	assertVolume(t, job.Spec.JobTemplate.Template.Spec.Volumes, clusterSSHVolumeName, "cluster-ssh")
	if job.Labels[niov1alpha1.LabelWorkloadName] != "prod" {
		t.Errorf("labels = %v, want the cluster's managed labels", job.Labels)
	}
}

// fakeJobLogs is a JobLogReader returning a canned log.
type fakeJobLogs struct {
	log   string
	err   error
	calls int
}

func (f *fakeJobLogs) JobLog(_ context.Context, _, _ string) (string, error) {
	f.calls++
	return f.log, f.err
}

// ---------------------------------------------------------------------------
// Teardown on deletion — envtest.
// ---------------------------------------------------------------------------

var _ = Describe("NixCluster teardown on deletion", func() {
	var counter int
	ctx := context.Background()
	var ns string

	BeforeEach(func() {
		counter++
		ns = fmt.Sprintf("cluster-teardown-%d", counter)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: ns},
		})).To(Succeed())
	})

	type fixture struct {
		req      reconcile.Request
		r        *NixClusterReconciler
		logs     *fakeJobLogs
		recorder *record.FakeRecorder
	}

	// setup creates a one-member cluster with the given policy (empty = API
	// default) and reconciles it once, so the finalizer and the converge child
	// exist the way they do on a live cluster.
	setup := func(name, policy string) fixture {
		Expect(k8sClient.Create(ctx, &niov1alpha1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-node-1", Namespace: ns, Labels: map[string]string{"cluster": name}},
			Spec:       niov1alpha1.MachineSpec{Host: "10.0.0.1"},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &niov1alpha1.NixCluster{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: niov1alpha1.NixClusterSpec{
				Source:         niov1alpha1.NixSource{GitRepo: "https://example.com/r", Ref: "main"},
				SSHKeyRef:      &niov1alpha1.SecretReference{Name: "cluster-ssh"},
				DeletionPolicy: policy,
				NodeGroups: []niov1alpha1.NodeGroup{{
					Name:     "nodes",
					Selector: metav1.LabelSelector{MatchLabels: map[string]string{"cluster": name}},
				}},
			},
		})).To(Succeed())
		rec := record.NewFakeRecorder(50)
		logs := &fakeJobLogs{}
		f := fixture{
			req:      reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}},
			r:        &NixClusterReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: rec, Logs: logs},
			logs:     logs,
			recorder: rec,
		}
		_, err := f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		return f
	}

	getCluster := func(name string) (*niov1alpha1.NixCluster, error) {
		var c niov1alpha1.NixCluster
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &c)
		return &c, err
	}
	getCron := func(name string) (*niov1alpha1.NixCronJob, error) {
		var c niov1alpha1.NixCronJob
		err := k8sClient.Get(ctx, types.NamespacedName{Name: convergeCronName(name), Namespace: ns}, &c)
		return &c, err
	}
	getTeardownJob := func(name string) (*niov1alpha1.NixJob, error) {
		var j niov1alpha1.NixJob
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name + "-teardown", Namespace: ns}, &j)
		return &j, err
	}
	countNixJobs := func() int {
		var list niov1alpha1.NixJobList
		Expect(k8sClient.List(ctx, &list, client.InNamespace(ns))).To(Succeed())
		return len(list.Items)
	}
	deleteCluster := func(name string) {
		c, err := getCluster(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(ctx, c)).To(Succeed())
	}
	expectGone := func(name string) {
		_, err := getCluster(name)
		Expect(err).To(HaveOccurred())
		Expect(client.IgnoreNotFound(err)).To(Succeed())
	}
	// finishTeardownJob simulates the NixJob controller observing the run-Job.
	finishTeardownJob := func(name, phase string) {
		j, err := getTeardownJob(name)
		Expect(err).NotTo(HaveOccurred())
		j.Status.Phase = phase
		j.Status.ActiveJob = name + "-teardown-abc123"
		if phase == niov1alpha1.PhaseReady {
			j.Status.Succeeded = 1
		} else {
			j.Status.Failed = 1
		}
		Expect(k8sClient.Status().Update(ctx, j)).To(Succeed())
	}
	resultLog := func(result, memberStatus, message string) string {
		return "evaluating...\n" + fmt.Sprintf(
			`::nixcluster:result:: {"cluster":"x","result":%q,"members":[{"name":"node-1","ip":"10.0.0.1","action":"destroyed","status":%q,"message":%q,"durationSeconds":1}],"steps":[]}`,
			result, memberStatus, message) + "\n"
	}

	It("defaults the deletion policy to Teardown", func() {
		setup("dflt", "")
		c, err := getCluster("dflt")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Spec.DeletionPolicy).To(Equal(niov1alpha1.DeletionPolicyTeardown))
	})

	It("Retain removes the converge child and the finalizer without a teardown run", func() {
		f := setup("keep", niov1alpha1.DeletionPolicyRetain)
		deleteCluster("keep")

		_, err := f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())

		expectGone("keep")
		_, err = getCron("keep")
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred())
		Expect(countNixJobs()).To(BeZero())
	})

	It("tears the cluster down, records the result and then releases the finalizer", func() {
		f := setup("happy", "")
		deleteCluster("happy")

		By("the first reconcile suspends converge and creates exactly one teardown run")
		_, err := f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())

		cron, err := getCron("happy")
		Expect(err).NotTo(HaveOccurred())
		Expect(cron.Spec.CronJobTemplate.Suspend).To(HaveValue(BeTrue()))
		Expect(cron.Spec.Nix.TriggerOnChange).To(HaveValue(BeFalse()))

		job, err := getTeardownJob("happy")
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Nix.Args).To(Equal([]string{"teardown", "--auto-approve"}))
		Expect(job.Spec.Nix.AdditionalFiles).To(Equal(cron.Spec.Nix.AdditionalFiles))
		Expect(metav1.IsControlledBy(job, &metav1.ObjectMeta{UID: mustUID("happy", ns)})).To(BeTrue())

		c, err := getCluster("happy")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Finalizers).To(ContainElement(niov1alpha1.FinalizerName))
		Expect(c.Status.Phase).To(Equal(niov1alpha1.NixClusterPhaseTearingDown))
		cond := meta.FindStatusCondition(c.Status.Conditions, niov1alpha1.ConditionTearingDown)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal(niov1alpha1.ReasonTeardownRunning))
		Expect(c.Status.Teardown).NotTo(BeNil())
		Expect(c.Status.Teardown.JobRef).To(Equal("happy-teardown"))
		Expect(c.Status.Teardown.Attempts).To(Equal(int32(1)))

		By("reconciling while the run is in progress creates no second run")
		for range 3 {
			res, rerr := f.r.Reconcile(ctx, f.req)
			Expect(rerr).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		}
		Expect(countNixJobs()).To(Equal(1))
		c, err = getCluster("happy")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Status.Teardown.Attempts).To(Equal(int32(1)))

		By("a successful run deletes the converge child and releases the finalizer")
		f.logs.log = resultLog("success", "Applied", "")
		finishTeardownJob("happy", niov1alpha1.PhaseReady)
		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())

		expectGone("happy")
		_, err = getCron("happy")
		Expect(err).To(HaveOccurred())
		Expect(f.logs.calls).To(BeNumerically(">", 0))
	})

	It("releases the finalizer on a partial result and warns about the skipped members", func() {
		f := setup("part", "")
		deleteCluster("part")
		_, err := f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())

		f.logs.log = resultLog("partial", "Skipped", "unreachable; its state is left as it is")
		finishTeardownJob("part", niov1alpha1.PhaseReady)
		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())

		expectGone("part")
		Expect(drainEvents(f.recorder)).To(ContainElement(And(
			ContainSubstring("Warning"), ContainSubstring("node-1"))))
	})

	It("follows the run's exit status when the result line cannot be read", func() {
		f := setup("nolog", "")
		deleteCluster("nolog")
		_, err := f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())

		f.logs.err = fmt.Errorf("pod is gone")
		finishTeardownJob("nolog", niov1alpha1.PhaseReady)
		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		expectGone("nolog")
	})

	It("keeps the finalizer on failure, records it once and retries after the backoff", func() {
		f := setup("fail", "")
		deleteCluster("fail")
		_, err := f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())

		f.logs.log = resultLog("failed", "Failed", "could not build, copy or switch to the base configuration")
		finishTeardownJob("fail", niov1alpha1.PhaseFailed)

		res, err := f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(res.RequeueAfter).To(BeNumerically("<=", teardownBackoffBase))

		c, err := getCluster("fail")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Finalizers).To(ContainElement(niov1alpha1.FinalizerName))
		Expect(c.Status.Phase).To(Equal(niov1alpha1.NixClusterPhaseTearingDown))
		failed := meta.FindStatusCondition(c.Status.Conditions, niov1alpha1.ConditionTeardownFailed)
		Expect(failed).NotTo(BeNil())
		Expect(failed.Status).To(Equal(metav1.ConditionTrue))
		Expect(failed.Message).To(ContainSubstring("could not build, copy or switch"))
		Expect(c.Status.Teardown.FailedAttempts).To(Equal(int32(1)))
		Expect(c.Status.Teardown.Result).To(Equal("failed"))
		Expect(c.Status.Teardown.Members).To(HaveLen(1))
		Expect(c.Status.Teardown.Members[0].Status).To(Equal("Failed"))
		tearing := meta.FindStatusCondition(c.Status.Conditions, niov1alpha1.ConditionTearingDown)
		Expect(tearing).NotTo(BeNil())
		Expect(tearing.Reason).To(Equal(niov1alpha1.ReasonRetryScheduled))

		By("during the backoff the failure is not recorded again and the failed run is kept")
		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		c, err = getCluster("fail")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Status.Teardown.FailedAttempts).To(Equal(int32(1)))
		_, err = getTeardownJob("fail")
		Expect(err).NotTo(HaveOccurred())

		By("once the backoff elapsed the failed run is replaced by a new one")
		c.Status.Teardown.LastFailureTime = &metav1.Time{Time: time.Now().Add(-time.Hour)}
		Expect(k8sClient.Status().Update(ctx, c)).To(Succeed())
		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		_, err = getTeardownJob("fail")
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred(), "the failed run is deleted first")

		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		job, err := getTeardownJob("fail")
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Status.Phase).To(BeEmpty(), "a fresh run, not the failed one")
		Expect(countNixJobs()).To(Equal(1))
		c, err = getCluster("fail")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Status.Teardown.Attempts).To(Equal(int32(2)))
		Expect(c.Finalizers).To(ContainElement(niov1alpha1.FinalizerName))
	})

	It("lets a failing teardown go when the policy is switched to Retain", func() {
		f := setup("flip", "")
		deleteCluster("flip")
		_, err := f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		f.logs.log = resultLog("failed", "Failed", "boom")
		finishTeardownJob("flip", niov1alpha1.PhaseFailed)
		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())

		c, err := getCluster("flip")
		Expect(err).NotTo(HaveOccurred())
		c.Spec.DeletionPolicy = niov1alpha1.DeletionPolicyRetain
		Expect(k8sClient.Update(ctx, c)).To(Succeed())

		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		expectGone("flip")
	})

	It("suspends converge and waits for it before the teardown run is created", func() {
		f := setup("wait", "")
		cron, err := getCron("wait")
		Expect(err).NotTo(HaveOccurred())

		// The projected CronJob, not yet suspended, and an active converge Job it
		// started — what the NixCronJob controller would have created.
		batchCron := &batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: cron.Name, Namespace: ns},
			Spec: batchv1.CronJobSpec{
				Schedule: "*/30 * * * *",
				JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers:    []corev1.Container{{Name: "app", Image: "nix"}},
					}},
				}},
			},
		}
		Expect(controllerutilSetOwner(cron, batchCron)).To(Succeed())
		Expect(k8sClient.Create(ctx, batchCron)).To(Succeed())
		activeJob := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: cron.Name + "-29000000", Namespace: ns},
			Spec:       batchCron.Spec.JobTemplate.Spec,
		}
		Expect(controllerutilSetOwner(batchCron, activeJob)).To(Succeed())
		Expect(k8sClient.Create(ctx, activeJob)).To(Succeed())

		deleteCluster("wait")

		By("the converge child is suspended, but its CronJob has not picked it up yet")
		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		cron, err = getCron("wait")
		Expect(err).NotTo(HaveOccurred())
		Expect(cron.Spec.CronJobTemplate.Suspend).To(HaveValue(BeTrue()))
		Expect(countNixJobs()).To(BeZero())

		By("the CronJob is suspended, but a converge run is still active")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(batchCron), batchCron)).To(Succeed())
		batchCron.Spec.Suspend = ptr(true)
		Expect(k8sClient.Update(ctx, batchCron)).To(Succeed())
		res, err := f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(BeNumerically(">", 0))
		Expect(countNixJobs()).To(BeZero())
		c, err := getCluster("wait")
		Expect(err).NotTo(HaveOccurred())
		cond := meta.FindStatusCondition(c.Status.Conditions, niov1alpha1.ConditionTearingDown)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal(niov1alpha1.ReasonWaitingForConverge))

		By("the converge run is gone, so the teardown run starts")
		Expect(k8sClient.Delete(ctx, activeJob)).To(Succeed())
		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		_, err = getTeardownJob("wait")
		Expect(err).NotTo(HaveOccurred())
		Expect(countNixJobs()).To(Equal(1))
	})

	It("releases the finalizer when there is no converge child to tear down", func() {
		f := setup("bare", "")
		cron, err := getCron("bare")
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Delete(ctx, cron)).To(Succeed())
		deleteCluster("bare")

		_, err = f.r.Reconcile(ctx, f.req)
		Expect(err).NotTo(HaveOccurred())
		expectGone("bare")
		Expect(countNixJobs()).To(BeZero())
	})
})

// mustUID returns the UID of a NixCluster that still exists.
func mustUID(name, namespace string) types.UID {
	var c niov1alpha1.NixCluster
	ExpectWithOffset(1, k8sClient.Get(context.Background(),
		types.NamespacedName{Name: name, Namespace: namespace}, &c)).To(Succeed())
	return c.UID
}

// controllerutilSetOwner makes owner the controller of obj using the suite scheme.
func controllerutilSetOwner(owner, obj client.Object) error {
	return controllerutil.SetControllerReference(owner, obj, k8sClient.Scheme())
}

// drainEvents returns every event recorded so far.
func drainEvents(rec *record.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}
