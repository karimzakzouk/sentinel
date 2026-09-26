package executors

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeRepoFetcher stubs RepoFileFetcher for discovery tests.
type fakeRepoFetcher struct {
	dirs   map[string][]DirEntry // path -> entries (dir listing)
	files  map[string]string     // path -> content
	calls  int
	failOn string
}

func (f *fakeRepoFetcher) ListDirEntries(_ context.Context, _ string, path, _ string) ([]DirEntry, error) {
	f.calls++
	if f.failOn == path {
		return nil, fmt.Errorf("simulated listing failure for %s", path)
	}
	if entries, ok := f.dirs[path]; ok {
		return entries, nil
	}
	return nil, fmt.Errorf("404 not found: %s", path)
}

func (f *fakeRepoFetcher) GetFileContentAt(_ context.Context, _ string, path, _ string) (string, error) {
	f.calls++
	if f.failOn == path {
		return "", fmt.Errorf("simulated fetch failure for %s", path)
	}
	if c, ok := f.files[path]; ok {
		return c, nil
	}
	return "", fmt.Errorf("404 not found: %s", path)
}

func TestExtractTestCommandsFromWorkflow(t *testing.T) {
	wf := `
name: CI
on: [push, pull_request]
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: cargo fmt --check
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: build
        run: cargo build --release
      - name: tests
        run: cargo test
  deploy:
    runs-on: ubuntu-latest
    steps:
      - run: docker push example/app:latest
`
	cmds := ExtractTestCommandsFromWorkflow(wf)
	joined := strings.Join(cmds, "|")
	// The "test" job's run steps are extracted in order.
	if !strings.Contains(joined, "cargo build --release") || !strings.Contains(joined, "cargo test") {
		t.Errorf("expected test job commands, got %q", joined)
	}
	// The lint job's id/name does not contain "test" and cargo fmt --check
	// matches the runner regex, so its steps may be included too — but the
	// deploy job's docker push must NEVER appear.
	if strings.Contains(joined, "docker push") {
		t.Errorf("deploy command leaked into test commands: %q", joined)
	}
}

func TestExtractTestCommandsWorkingDir(t *testing.T) {
	wf := `
jobs:
  unittest:
    steps:
      - run: npm test
        working-directory: services/api
`
	cmds := ExtractTestCommandsFromWorkflow(wf)
	if len(cmds) == 0 {
		t.Fatal("expected one command")
	}
	if !strings.HasPrefix(cmds[0], "cd services/api && ") {
		t.Errorf("working-directory not honored: %q", cmds[0])
	}
}

func TestExtractTestCommandsInvalidYAML(t *testing.T) {
	if got := ExtractTestCommandsFromWorkflow("not: [valid: yaml"); len(got) != 0 {
		t.Errorf("invalid yaml should produce no commands, got %v", got)
	}
	if got := ExtractTestCommandsFromWorkflow(""); len(got) != 0 {
		t.Errorf("empty yaml should produce no commands, got %v", got)
	}
}

func TestExtractTestCommandsJobNameDetection(t *testing.T) {
	// Job id doesn't mention test, name does.
	wf := `
jobs:
  build:
    name: Unit Tests
    steps:
      - run: go test ./...
`
	cmds := ExtractTestCommandsFromWorkflow(wf)
	if len(cmds) != 1 || cmds[0] != "go test ./..." {
		t.Errorf("expected [go test ./...], got %v", cmds)
	}
}

func TestDiscoverRepoTestCommandsFromCI(t *testing.T) {
	fetcher := &fakeRepoFetcher{
		dirs: map[string][]DirEntry{
			".github/workflows": {
				{Name: "ci.yml", Path: ".github/workflows/ci.yml", Type: "file"},
				{Name: "deploy.yml", Path: ".github/workflows/deploy.yml", Type: "file"},
				{Name: "README.md", Path: ".github/workflows/README.md", Type: "file"},
			},
		},
		files: map[string]string{
			".github/workflows/ci.yml":     "jobs:\n  test:\n    steps:\n      - run: npm ci\n      - run: npm test\n",
			".github/workflows/deploy.yml": "jobs:\n  release:\n    steps:\n      - run: docker push x\n",
		},
	}
	cmds, source := DiscoverRepoTestCommands(context.Background(), fetcher, "org/repo", "main")
	if source != "ci-yaml" {
		t.Fatalf("expected source ci-yaml, got %q", source)
	}
	if strings.Join(cmds, "&&") != "npm ci&&npm test" {
		t.Errorf("unexpected commands: %v", cmds)
	}
}

func TestDiscoverRepoTestCommandsInference(t *testing.T) {
	cases := []struct {
		name    string
		fetcher *fakeRepoFetcher
		want    string
		wantSrc string
	}{
		{
			name:    "pom.xml -> mvn",
			fetcher: &fakeRepoFetcher{files: map[string]string{"pom.xml": "<project/>"}},
			want:    "mvn -B test", wantSrc: "inferred",
		},
		{
			name:    "Cargo.toml -> cargo",
			fetcher: &fakeRepoFetcher{files: map[string]string{"Cargo.toml": "[package]"}},
			want:    "cargo test", wantSrc: "inferred",
		},
		{
			name:    "go.mod -> go test",
			fetcher: &fakeRepoFetcher{files: map[string]string{"go.mod": "module x"}},
			want:    "go test ./...", wantSrc: "inferred",
		},
		{
			name: "package.json with test script",
			fetcher: &fakeRepoFetcher{files: map[string]string{
				"package.json": `{"scripts":{"test":"jest"}}`,
			}},
			want: "npm ci && npm test", wantSrc: "inferred",
		},
		{
			name: "package.json without test script -> nothing",
			fetcher: &fakeRepoFetcher{files: map[string]string{
				"package.json": `{"scripts":{"start":"node x.js"}}`,
			}},
			want: "", wantSrc: "",
		},
		{
			name: "Gemfile + spec/ -> rspec",
			fetcher: &fakeRepoFetcher{
				dirs:  map[string][]DirEntry{"spec": {{Name: "app_spec.rb", Type: "file"}}},
				files: map[string]string{"Gemfile": "source 'rubygems'"},
			},
			want: "bundle install && bundle exec rspec", wantSrc: "inferred",
		},
		{
			name:    "no markers at all -> nothing",
			fetcher: &fakeRepoFetcher{},
			want:    "", wantSrc: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmds, source := DiscoverRepoTestCommands(context.Background(), tc.fetcher, "org/repo", "main")
			if source != tc.wantSrc {
				t.Fatalf("source = %q, want %q", source, tc.wantSrc)
			}
			if tc.want == "" {
				if len(cmds) != 0 {
					t.Fatalf("expected no commands, got %v", cmds)
				}
				return
			}
			if len(cmds) != 1 || cmds[0] != tc.want {
				t.Fatalf("commands = %v, want [%s]", cmds, tc.want)
			}
		})
	}
}

func TestDefaultBuilderImageForCommands(t *testing.T) {
	cases := []struct {
		cmds []string
		want string
	}{
		{[]string{"cargo test"}, "rust:1"},
		{[]string{"mvn -B test"}, "maven:3.9-eclipse-temurin-17"},
		{[]string{"gradle test"}, "gradle:8-jdk17"},
		{[]string{"dotnet test"}, "mcr.microsoft.com/dotnet/sdk:8.0"},
		{[]string{"go test ./..."}, "golang:1.23-bookworm"},
		{[]string{"bundle exec rspec"}, "ruby:3.2"},
		{[]string{"python -m pytest -q"}, "python:3.11"},
		{[]string{"npm ci", "npm test"}, "node:20"},
		{[]string{"make sandwich"}, ""},
	}
	for _, tc := range cases {
		if got := defaultBuilderImageForCommands(tc.cmds); got != tc.want {
			t.Errorf("builderImage(%v) = %q, want %q", tc.cmds, got, tc.want)
		}
	}
}

func TestNpmTestCommand(t *testing.T) {
	if got := npmTestCommand(`{"scripts":{"test":"jest --coverage"}}`); got != "npm ci && npm test" {
		t.Errorf("got %q", got)
	}
	if got := npmTestCommand(`{"scripts":{}}`); got != "" {
		t.Errorf("no test script should yield empty, got %q", got)
	}
	if got := npmTestCommand(`{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`); got != "" {
		t.Errorf("npm default placeholder should yield empty, got %q", got)
	}
	if got := npmTestCommand("not json"); got != "" {
		t.Errorf("invalid json should yield empty, got %q", got)
	}
}

func TestRepoTestOutcomeSkipOnNoCommands(t *testing.T) {
	r := NewRepoTestRunner(nil, "sentinel")
	outcome := r.Run(context.Background(), &fakeRepoFetcher{}, "org/repo", "sentinel/x-abc", "main")
	if outcome == nil || !outcome.Skipped {
		t.Fatalf("expected skipped outcome, got %+v", outcome)
	}
}
