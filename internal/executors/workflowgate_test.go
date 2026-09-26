package executors

import (
	"os/exec"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// The fixtures below are the REAL .github/workflows/ci.yml files from the
// eval service repos (fetched 2026-09-26). They are the gate's primary
// parsing targets — if the parser regresses on any of them, pre-push
// verification silently stops covering that ecosystem.

const fixtureNodeCI = `name: CI

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Node
        uses: actions/setup-node@v4
        with:
          node-version: "20"
          cache: "npm"

      - name: Install dependencies
        run: npm ci

      - name: Type check
        run: npx tsc --noEmit

      - name: Test
        run: npm test
        continue-on-error: true # no tests yet — don't block CI
`

const fixturePythonCI = `name: CI

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Python
        uses: actions/setup-python@v5
        with:
          python-version: "3.12"

      - name: Install dependencies
        run: |
          python -m pip install --upgrade pip
          pip install -r requirements.txt

      - name: Lint
        run: ruff check app/ tests/

      - name: Test
        run: pytest tests/ -v --tb=short
`

const fixtureGoCI = `name: CI

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: "1.22"

      - name: Download modules
        run: go mod download

      - name: Vet
        run: go vet ./...

      - name: Test
        run: go test ./... -v -race
`

const fixtureJavaCI = `name: CI

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up JDK 21
        uses: actions/setup-java@v4
        with:
          distribution: temurin
          java-version: "21"
          cache: maven

      - name: Compile
        run: mvn -B -q compile

      - name: Test
        run: mvn -B test
`

const fixtureRustCI = `name: CI

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Rust
        uses: dtolnay/rust-toolchain@stable
        with:
          toolchain: 1.78.0
          components: rustfmt, clippy

      - name: Cache cargo
        uses: actions/cache@v4
        with:
          path: |
            ~/.cargo/registry
            ~/.cargo/git
            target
          key: ${{ runner.os }}-cargo-${{ hashFiles('Cargo.toml') }}

      - name: Format check
        run: cargo fmt --all -- --check

      - name: Clippy
        run: cargo clippy --all-targets -- -D warnings

      - name: Test
        run: cargo test --all --verbose
`

const fixtureRubyCI = `name: CI

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Ruby
        uses: ruby/setup-ruby@v1
        with:
          ruby-version: "3.3"
          bundler-cache: true

      - name: RuboCop
        run: bundle exec rubocop --fail-level warning || true

      - name: Test
        run: bundle exec rspec spec --format documentation
`

const fixtureCppCI = `name: CI

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install toolchain
        run: |
          sudo apt-get update
          sudo apt-get install -y cmake ninja-build

      - name: Configure
        run: cmake -S . -B build -G Ninja -DCMAKE_BUILD_TYPE=Debug

      - name: Build
        run: cmake --build build --parallel "$(nproc)"

      - name: Test
        run: ctest --test-dir build --output-on-failure
`

// fixtureDeployWorkflow is push-only (no pull_request trigger): image
// build + deploy steps must NOT be reproduced by the gate.
const fixtureDeployWorkflow = `name: Deploy

on:
  push:
    branches: [main]

permissions:
  contents: write
  packages: write

jobs:
  build-and-deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Build and push
        run: |
          echo "${{ secrets.GITHUB_TOKEN }}" | docker login ghcr.io -u dummy --password-stdin
          docker buildx build --push -t ghcr.io/org/app:latest .
`

func TestParseWorkflowEcosystems(t *testing.T) {
	tests := []struct {
		name      string
		fixture   string
		wantImage string
		wantSteps []string
		wantBoot  string
		tolerant  []bool // expected ContinueOnError per step (nil = all false)
	}{
		{
			name:      "node (user-api)",
			fixture:   fixtureNodeCI,
			wantImage: "node:20",
			wantSteps: []string{"Install dependencies", "Type check", "Test"},
			tolerant:  []bool{false, false, true},
		},
		{
			name:      "python (notification-worker)",
			fixture:   fixturePythonCI,
			wantImage: "python:3.12",
			wantSteps: []string{"Install dependencies", "Lint", "Test"},
		},
		{
			name:      "go (payments-api)",
			fixture:   fixtureGoCI,
			wantImage: "golang:1.22",
			wantSteps: []string{"Download modules", "Vet", "Test"},
		},
		{
			name:      "java (shipping-api)",
			fixture:   fixtureJavaCI,
			wantImage: "maven:3.9-eclipse-temurin-21",
			wantSteps: []string{"Compile", "Test"},
		},
		{
			name:      "rust (search-api)",
			fixture:   fixtureRustCI,
			wantImage: "rust:1.78.0",
			wantSteps: []string{"Format check", "Clippy", "Test"},
			wantBoot:  "rustup component add rustfmt clippy 2>/dev/null || true",
		},
		{
			name:      "ruby (analytics-worker)",
			fixture:   fixtureRubyCI,
			wantImage: "ruby:3.3",
			wantSteps: []string{"RuboCop", "Test"},
			wantBoot:  "bundle install --jobs 4",
		},
		{
			name:      "cpp (recommendation-engine)",
			fixture:   fixtureCppCI,
			wantImage: "ubuntu:24.04",
			wantSteps: []string{"Install toolchain", "Configure", "Build", "Test"},
			wantBoot:  "apt-get install -y -qq --no-install-recommends cmake ninja-build g++",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plans, _ := parseWorkflow("ci.yml", tt.fixture, 30)
			if len(plans) != 1 {
				t.Fatalf("expected 1 plan, got %d", len(plans))
			}
			plan := plans[0]
			if plan.Job != "test" {
				t.Errorf("job = %q, want test", plan.Job)
			}
			if plan.Image != tt.wantImage {
				t.Errorf("image = %q, want %q", plan.Image, tt.wantImage)
			}
			if len(plan.Steps) != len(tt.wantSteps) {
				t.Fatalf("steps = %v, want %v", stepNames(plan), tt.wantSteps)
			}
			for i, want := range tt.wantSteps {
				if plan.Steps[i].Name != want {
					t.Errorf("step[%d].Name = %q, want %q", i, plan.Steps[i].Name, want)
				}
			}
			if tt.wantBoot != "" && !strings.Contains(plan.Bootstrap, tt.wantBoot) {
				t.Errorf("bootstrap = %q, want it to contain %q", plan.Bootstrap, tt.wantBoot)
			}
			if tt.tolerant != nil {
				for i, want := range tt.tolerant {
					if plan.Steps[i].ContinueOnError != want {
						t.Errorf("step[%d].ContinueOnError = %v, want %v", i, plan.Steps[i].ContinueOnError, want)
					}
				}
			}
		})
	}
}

func stepNames(plan gatePlan) []string {
	names := make([]string, len(plan.Steps))
	for i, s := range plan.Steps {
		names[i] = s.Name
	}
	return names
}

// The node fixture's multi-command "Install dependencies" step (none here)
// and the python fixture's multi-line pip step must survive verbatim.
func TestParseWorkflowMultiLineRunPreserved(t *testing.T) {
	plans, _ := parseWorkflow("ci.yml", fixturePythonCI, 30)
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	run := plans[0].Steps[0].Run
	for _, want := range []string{"python -m pip install --upgrade pip", "pip install -r requirements.txt"} {
		if !strings.Contains(run, want) {
			t.Errorf("multi-line run lost %q; got:\n%s", want, run)
		}
	}
}

// Deploy workflows (push-only) must be excluded — running buildx/registry
// login steps would be both useless and secret-leaking.
func TestParseWorkflowExcludesDeployWorkflows(t *testing.T) {
	plans, notes := parseWorkflow("deploy.yml", fixtureDeployWorkflow, 30)
	if len(plans) != 0 {
		t.Fatalf("push-only workflow produced %d plans, want 0", len(plans))
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "no pull_request trigger") {
		t.Errorf("expected exclusion note, got %v", notes)
	}
}

func TestParseWorkflowSkipsExpressionSteps(t *testing.T) {
	wf := `name: CI
on: [pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Real
        run: npm test
      - name: Expr
        run: make deploy ENV=${{ secrets.ENV }}
`
	plans, notes := parseWorkflow("ci.yml", wf, 30)
	if len(plans) != 1 || len(plans[0].Steps) != 1 {
		t.Fatalf("expected 1 step, got %d (notes: %v)", len(plans), notes)
	}
	if plans[0].Steps[0].Name != "Real" {
		t.Errorf("kept wrong step: %q", plans[0].Steps[0].Name)
	}
}

func TestParseWorkflowMatrixSkipped(t *testing.T) {
	wf := `name: CI
on: pull_request
jobs:
  test:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        node: [18, 20]
    steps:
      - run: npm test
`
	plans, notes := parseWorkflow("ci.yml", wf, 30)
	if len(plans) != 0 {
		t.Fatalf("matrix job produced plans, want 0")
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "matrix") {
		t.Errorf("expected matrix note, got %v", notes)
	}
}

func TestParseWorkflowStepCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("name: CI\non: pull_request\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n")
	for i := 0; i < 10; i++ {
		b.WriteString("      - run: echo hi\n")
	}
	plans, _ := parseWorkflow("ci.yml", b.String(), 3)
	if len(plans) != 1 || len(plans[0].Steps) != 3 {
		t.Fatalf("step cap not enforced: %d steps", len(plans[0].Steps))
	}
}

func TestSanitizeVersion(t *testing.T) {
	cases := map[string]string{
		"20":      "20",
		"\"20\"":  "20",
		"20.x":    "20",
		">=3.12":  "3.12",
		"1.78.0":  "1.78.0",
		"stable":  "",
		"":        "",
		"v1.22.3": "1.22.3",
	}
	for in, want := range cases {
		got := versionRe.FindString(in)
		if got != want {
			t.Errorf("sanitizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// The assembled script must be directly inspectable: banners present,
// bootstrap first, continue-on-error wrapped tolerantly, env exported.
func TestBuildGateScript(t *testing.T) {
	plans, _ := parseWorkflow("ci.yml", fixtureNodeCI, 30)
	script := buildGateScript(plans[0])

	for _, want := range []string{
		"#!/bin/sh",
		"set -e",
		"cd /workspace/repo",
		"=== GATE STEP 1/3: Install dependencies ===",
		"( npm ci )",
		"( npx tsc --noEmit )",
		"=== GATE STEP 3/3: Test ===",
		"continue-on-error",
		"workflow-gate: ALL STEPS COMPLETED",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q;\n--- script ---\n%s", want, script)
		}
	}
}

func TestBuildGateScriptWorkingDir(t *testing.T) {
	plan := gatePlan{
		Workflow: "ci.yml",
		Job:      "test",
		Image:    "node:20",
		Steps: []gateStep{{
			Name:       "Monorepo test",
			Run:        "npm test",
			WorkingDir: "packages/app",
		}},
	}
	script := buildGateScript(plan)
	if !strings.Contains(script, "cd 'packages/app'") {
		t.Errorf("working-directory not applied:\n%s", script)
	}
}

func TestExtractGateFailure(t *testing.T) {
	plan := gatePlan{
		Workflow: "ci.yml",
		Job:      "test",
		Steps: []gateStep{
			{Name: "Install", Run: "npm ci"},
			{Name: "Test", Run: "npm test"},
		},
	}
	logs := `=== GATE STEP 1/2: Install ===
added 312 packages
=== GATE STEP 2/2: Test ===
FAIL src/app.test.ts
  ● app › renders
    TypeError: Cannot read properties of undefined
      at Object.<anonymous> (src/app.test.ts:12:19)
Tests: 1 failed, 1 passed
`
	stepName, stepCmd, output := extractGateFailure(logs, plan)
	if stepName != "Test" {
		t.Errorf("stepName = %q, want Test", stepName)
	}
	if stepCmd != "npm test" {
		t.Errorf("stepCmd = %q, want npm test", stepCmd)
	}
	if !strings.Contains(output, "TypeError") {
		t.Errorf("output should contain the failure:\n%s", output)
	}
	if strings.Contains(output, "added 312 packages") {
		t.Errorf("output should start at the failing step banner:\n%s", output)
	}
}

func TestGateRunID(t *testing.T) {
	id := gateRunID("sentinel/user-api-ab12cd34")
	if !strings.HasPrefix(id, "user-api-ab12cd34-") {
		t.Errorf("gateRunID = %q", id)
	}
	if len(id) > 63-14 { // 63 DNS limit minus "workflow-gate-" prefix
		t.Errorf("id too long for pod name: %q", id)
	}
	if gateRunID("weird/../branch name!") == "" {
		t.Errorf("expected sanitized non-empty id")
	}
}

func TestHasPullRequestTriggerForms(t *testing.T) {
	forms := []string{
		"on: pull_request",
		"on: [pull_request, push]",
		"on:\n  pull_request:\n    branches: [main]",
		"on:\n  push:\n    branches: [main]\n  pull_request:",
	}
	for _, f := range forms {
		var root wfRoot
		if err := yaml.Unmarshal([]byte(f), &root); err != nil {
			t.Fatalf("unmarshal %q: %v", f, err)
		}
		if !hasPullRequestTrigger(&root.On) {
			t.Errorf("hasPullRequestTrigger(%q) = false, want true", f)
		}
	}
	negatives := []string{
		"on: push",
		"on:\n  push:\n    branches: [main]",
		"on: schedule",
	}
	for _, f := range negatives {
		var root wfRoot
		if err := yaml.Unmarshal([]byte(f), &root); err != nil {
			t.Fatalf("unmarshal %q: %v", f, err)
		}
		if hasPullRequestTrigger(&root.On) {
			t.Errorf("hasPullRequestTrigger(%q) = true, want false", f)
		}
	}
}

// Quoted and unquoted versions must decode identically (go-version: 1.22
// is a YAML float; node-version: "20" a string — nodeText handles both).
func TestNodeTextUnquotedFloat(t *testing.T) {
	var step wfStep
	doc := `
name: x
with:
  go-version: 1.22
  node-version: "20"
  toolchain: 1.78.0
`
	if err := yaml.Unmarshal([]byte(doc), &step); err != nil {
		t.Fatal(err)
	}
	if got := nodeText(step.With["go-version"]); got != "1.22" {
		t.Errorf("go-version = %q, want 1.22", got)
	}
	if got := nodeText(step.With["node-version"]); got != "20" {
		t.Errorf("node-version = %q, want 20", got)
	}
	if got := nodeText(step.With["toolchain"]); got != "1.78.0" {
		t.Errorf("toolchain = %q, want 1.78.0", got)
	}
}

// TestBuildGateScriptIsValidSh pipes every fixture's generated script
// through `sh -n` (syntax check, no execution). A malformed script would
// kill every gate run for that ecosystem with a confusing syntax error.
func TestBuildGateScriptIsValidSh(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	fixtures := map[string]string{
		"node":   fixtureNodeCI,
		"python": fixturePythonCI,
		"go":     fixtureGoCI,
		"java":   fixtureJavaCI,
		"rust":   fixtureRustCI,
		"ruby":   fixtureRubyCI,
		"cpp":    fixtureCppCI,
	}
	for eco, fx := range fixtures {
		plans, _ := parseWorkflow("ci.yml", fx, 30)
		if len(plans) != 1 {
			t.Fatalf("%s: expected 1 plan", eco)
		}
		script := buildGateScript(plans[0])
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: sh -n rejected script: %v\n%s\n--- script ---\n%s", eco, err, out, script)
		}
	}
}
