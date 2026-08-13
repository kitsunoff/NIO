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

// Package buildmeta holds no production code. It exists so that the repository's
// build metadata can be asserted by the ordinary `go test ./internal/...` run.
package buildmeta

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// repoRoot is this package's directory, two levels below the repository root.
const repoRoot = "../.."

var (
	// toolchainRe matches the go.mod toolchain directive, e.g. "toolchain go1.26.5".
	toolchainRe = regexp.MustCompile(`(?m)^toolchain\s+go(\S+)\s*$`)
	// builderRe matches the Dockerfile builder stage, e.g. "FROM golang:1.26.5 AS builder".
	builderRe = regexp.MustCompile(`(?mi)^FROM\s+golang:(\S+)\s+AS\s+builder\s*$`)
)

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestGoModPinsAToolchain fails if the toolchain directive is dropped from
// go.mod. Without it, a builder whose local Go is older than the version that
// fixes the standard-library advisories silently produces a vulnerable binary,
// which is the regression the v1.0.1 chain exists to close.
func TestGoModPinsAToolchain(t *testing.T) {
	m := toolchainRe.FindStringSubmatch(readRepoFile(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod has no `toolchain goX.Y.Z` directive; " +
			"removing it lets an older standard library back into the build")
	}
	t.Logf("go.mod toolchain: go%s", m[1])
}

// TestDockerfileBuilderMatchesGoModToolchain fails if the published image would
// be built by a different standard library than the one CI verifies with
// govulncheck. The two pins are in separate files, so nothing but this test
// stops them drifting apart.
func TestDockerfileBuilderMatchesGoModToolchain(t *testing.T) {
	modMatch := toolchainRe.FindStringSubmatch(readRepoFile(t, "go.mod"))
	if modMatch == nil {
		t.Fatal("go.mod has no `toolchain goX.Y.Z` directive")
	}
	dockerMatch := builderRe.FindStringSubmatch(readRepoFile(t, "Dockerfile"))
	if dockerMatch == nil {
		t.Fatal("Dockerfile has no `FROM golang:<version> AS builder` stage")
	}
	if modMatch[1] != dockerMatch[1] {
		t.Fatalf("build toolchain drift: go.mod pins go%s but the Dockerfile builder is golang:%s; "+
			"the published binary would not be built by the toolchain govulncheck verifies",
			modMatch[1], dockerMatch[1])
	}
	t.Logf("go.mod toolchain and Dockerfile builder agree on go%s", modMatch[1])
}
