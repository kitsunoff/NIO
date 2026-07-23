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

package applyjob

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestInjectedFilesReachNixBuild proves end-to-end, against a real Nix, that an
// injected additionalFile only reaches the flake build once it is staged: an
// untracked file is dropped from the store copy (build fails), and after the
// runner's `git add --force` step the same file is visible. Skipped when nix or
// git is unavailable (e.g. CI without Nix).
func TestInjectedFilesReachNixBuild(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	mustRun := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}

	// A flake whose output reads a file that will be injected, not committed.
	flake := `{
  outputs = { self }: { injected = builtins.readFile ./injected.nix; };
}
`
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(flake), 0o644); err != nil {
		t.Fatalf("write flake.nix: %v", err)
	}
	mustRun("git", "init", "-q")
	mustRun("git", "config", "user.email", "t@example.com")
	mustRun("git", "config", "user.name", "t")
	mustRun("git", "add", "flake.nix")
	mustRun("git", "commit", "-q", "-m", "init")

	// Inject an untracked file exactly as the runner does.
	runner := NewRunner(dir)
	if err := runner.InjectAdditionalFiles(dir, []AdditionalFile{
		{Path: "injected.nix", Content: "HELLO_FROM_INJECTED"},
	}); err != nil {
		t.Fatalf("InjectAdditionalFiles: %v", err)
	}

	evalInjected := func() (string, error) {
		cmd := exec.CommandContext(context.Background(), "nix", "eval",
			"--extra-experimental-features", "nix-command flakes",
			"--no-write-lock-file", "--raw", dir+"#injected")
		// stdout only — the "Git tree is dirty" warning goes to stderr.
		out, err := cmd.Output()
		return string(out), err
	}

	// Before staging: the untracked file is not in the store copy, so the build
	// cannot read it. This is the bug this change fixes.
	if _, err := evalInjected(); err == nil {
		t.Fatal("expected the build to fail for an untracked injected file, but it succeeded")
	}

	// The runner stages injected files; after that Nix includes them.
	mustRun("git", "add", "--force", "--", "injected.nix")
	out, err := evalInjected()
	if err != nil {
		t.Fatalf("nix eval after staging failed: %v\n%s", err, out)
	}
	if out != "HELLO_FROM_INJECTED" {
		t.Errorf("built value = %q, want the injected content", out)
	}
}

// TestInjectedGitignoredFileReachesNixBuild proves that force-staging makes an
// injected file reach the build even when the repo's .gitignore would skip it
// (a plain `git add` / `git add --all` silently drops ignored paths).
func TestInjectedGitignoredFileReachesNixBuild(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	mustRun := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}

	flake := `{
  outputs = { self }: { injected = builtins.readFile ./secret.nix; };
}
`
	if err := os.WriteFile(filepath.Join(dir, "flake.nix"), []byte(flake), 0o644); err != nil {
		t.Fatalf("write flake.nix: %v", err)
	}
	// The repo gitignores exactly the path the operator injects.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secret.nix\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	mustRun("git", "init", "-q")
	mustRun("git", "config", "user.email", "t@example.com")
	mustRun("git", "config", "user.name", "t")
	mustRun("git", "add", "flake.nix", ".gitignore")
	mustRun("git", "commit", "-q", "-m", "init")

	runner := NewRunner(dir)
	if err := runner.InjectAdditionalFiles(dir, []AdditionalFile{
		{Path: "secret.nix", Content: "SECRET_VALUE"},
	}); err != nil {
		t.Fatalf("InjectAdditionalFiles: %v", err)
	}

	// A plain `git add --all` would skip the gitignored path; --force stages it.
	mustRun("git", "add", "--force", "--", "secret.nix")

	cmd := exec.Command("nix", "eval",
		"--extra-experimental-features", "nix-command flakes",
		"--no-write-lock-file", "--raw", dir+"#injected")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("nix eval failed for force-staged gitignored file: %v", err)
	}
	if string(out) != "SECRET_VALUE" {
		t.Errorf("built value = %q, want the injected gitignored content", out)
	}
}
