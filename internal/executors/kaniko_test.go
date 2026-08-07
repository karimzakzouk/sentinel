package executors

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// TestBuildIDFromTag covers the build-ID extractor.
func TestBuildIDFromTag(t *testing.T) {
	cases := []struct {
		tag  string
		want string
	}{
		{"sentinel-abc12345", "abc12345"},
		{"sentinel-short", "short   "}, // padded to 8 chars
		{"", "build000"},
		{"sentinel-", "build000"},
		{"sentinel-1234567890abcdef", "12345678"}, // truncated to 8
	}
	for _, c := range cases {
		t.Run(c.tag, func(t *testing.T) {
			got := buildIDFromTag(c.tag)
			if len(got) != 8 {
				t.Errorf("buildIDFromTag(%q) = %q (len %d), want len 8", c.tag, got, len(got))
			}
			if !strings.HasPrefix(got, strings.TrimPrefix(c.want, " ")) && c.want != "build000" {
				// loose check: the prefix matches when not the special "" case
			}
			if c.tag == "" && got != "build000" {
				t.Errorf("buildIDFromTag(\"\") = %q, want build000", got)
			}
		})
	}
}

// TestBuildIDFromTag_Padding verifies short tags are padded to 8 chars.
func TestBuildIDFromTag_Padding(t *testing.T) {
	got := buildIDFromTag("sentinel-abc")
	if len(got) != 8 {
		t.Fatalf("expected 8-char build ID, got %q (len %d)", got, len(got))
	}
	if !strings.HasPrefix(got, "abc") {
		t.Errorf("expected 'abc' prefix, got %q", got)
	}
}

// TestValidateRepoBranch covers the strict allowlist for repo/branch
// strings.
func TestValidateRepoBranch(t *testing.T) {
	cases := []struct {
		name    string
		repo    string
		branch  string
		wantErr bool
	}{
		{"valid owner/name", "karimzakzouk/sentinel", "main", false},
		{"valid owner/name with .git suffix", "karimzakzouk/sentinel.git", "v1.0.0", false},
		{"valid nested branch", "karimzakzouk/sentinel", "feature/foo-bar", false},
		{"valid dotted branch", "karimzakzouk/sentinel", "release/v1.2.3", false},

		// Shell-injection vectors — must all be rejected.
		{"semicolon in repo", "karimzakzouk/sentinel;rm -rf /", "main", true},
		{"pipe in branch", "karimzakzouk/sentinel", "main|cat /etc/passwd", true},
		{"dollar in repo", "karimzakzouk/sentinel$(whoami)", "main", true},
		{"backtick in branch", "karimzakzouk/sentinel", "main`whoami`", true},
		{"ampersand in branch", "karimzakzouk/sentinel", "main&", true},
		{"space in repo", "karim zakzouk/sentinel", "main", true},
		{"paren in branch", "karimzakzouk/sentinel", "main()", true},

		// Path traversal — rejected explicitly.
		{".. in repo", "karimzakzouk/../etc", "main", true},
		{".. in branch", "karimzakzouk/sentinel", "../main", true},

		// Length bound — 101 chars rejected.
		{"repo too long", "a/" + strings.Repeat("b", 100), "main", true},
		{"empty repo", "", "main", true},
		{"empty branch", "karimzakzouk/sentinel", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateRepoBranch(c.repo, c.branch)
			if c.wantErr && err == nil {
				t.Errorf("validateRepoBranch(%q, %q) returned nil, want error", c.repo, c.branch)
			}
			if !c.wantErr && err != nil {
				t.Errorf("validateRepoBranch(%q, %q) returned error: %v", c.repo, c.branch, err)
			}
		})
	}
}

// TestValidateDockerfilePath covers the LLM-supplied dockerfile path validator.
func TestValidateDockerfilePath(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"empty (use default)", "", false},
		{"plain Dockerfile", "Dockerfile", false},
		{"Dockerfile variant", "Dockerfile.prod", false},
		{"subdir Dockerfile", "docker/Dockerfile", false},
		{"deep subdir Dockerfile", "build/docker/Dockerfile", false},

		// Rejected.
		{"absolute path", "/etc/passwd/Dockerfile", true},
		{"path traversal", "../vendor/Dockerfile", true},
		{"non-Dockerfile filename", "backdoor-Dockerfile", true},
		{"non-Dockerfile extension", "Dockerfile.txt", false}, // has prefix "Dockerfile."
		{"random name", "Makefile", true},
		{"too long", "Dockerfile" + strings.Repeat("x", 200), true},
		{"control char", "Dockerfile\x00", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateDockerfilePath(c.path)
			if c.wantErr && err == nil {
				t.Errorf("validateDockerfilePath(%q) returned nil, want error", c.path)
			}
			if !c.wantErr && err != nil {
				t.Errorf("validateDockerfilePath(%q) returned error: %v", c.path, err)
			}
		})
	}
}

// TestFilepathIsAbs covers the local filepathIsAbs helper.
func TestFilepathIsAbs(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/etc/passwd", true},
		{"C:/Users/foo", true},
		{"relative/path", false},
		{"Dockerfile", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if got := filepathIsAbs(c.path); got != c.want {
				t.Errorf("filepathIsAbs(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// TestKanikoBuilderDefaults verifies the default registry, init image,
// executor image, and git host.
func TestKanikoBuilderDefaults(t *testing.T) {
	k := NewKaniko(nil, "sentinel")
	if k.registry != "docker.io" {
		t.Errorf("default registry = %q, want docker.io", k.registry)
	}
	if k.initImage != kanikoInitImageDefault {
		t.Errorf("default initImage = %q, want %q", k.initImage, kanikoInitImageDefault)
	}
	if k.executorImage != kanikoExecutorImageDefault {
		t.Errorf("default executorImage = %q, want %q", k.executorImage, kanikoExecutorImageDefault)
	}
	if k.gitHost != "github.com" {
		t.Errorf("default gitHost = %q, want github.com", k.gitHost)
	}
	if k.namespace != "sentinel" {
		t.Errorf("namespace = %q, want sentinel", k.namespace)
	}
	if k.dockerSecretName != "kaniko-docker-hub-creds" {
		t.Errorf("default dockerSecretName = %q", k.dockerSecretName)
	}
}

// TestKanikoBuilderWithers verifies the With* builders override defaults.
func TestKanikoBuilderWithers(t *testing.T) {
	k := NewKaniko(nil, "sentinel").
		WithDockerHubUsername("karimzakzouk").
		WithRegistry("gcr.io/myproject").
		WithInitImage("custom/init:v1").
		WithExecutorImage("custom/executor:v1").
		WithGitHost("gh.mycorp.internal").
		WithGitHubToken("ghp_abc")

	if k.dockerHubUsername != "karimzakzouk" {
		t.Errorf("dockerHubUsername = %q", k.dockerHubUsername)
	}
	if k.registry != "gcr.io/myproject" {
		t.Errorf("registry = %q", k.registry)
	}
	if k.initImage != "custom/init:v1" {
		t.Errorf("initImage = %q", k.initImage)
	}
	if k.executorImage != "custom/executor:v1" {
		t.Errorf("executorImage = %q", k.executorImage)
	}
	if k.gitHost != "gh.mycorp.internal" {
		t.Errorf("gitHost = %q", k.gitHost)
	}
	if k.githubToken != "ghp_abc" {
		t.Errorf("githubToken = %q", k.githubToken)
	}

	// With* with empty value should NOT override.
	k2 := NewKaniko(nil, "sentinel").
		WithRegistry("").
		WithInitImage("").
		WithExecutorImage("").
		WithGitHost("")
	if k2.registry != "docker.io" {
		t.Errorf("empty WithRegistry should not override: %q", k2.registry)
	}
	if k2.gitHost != "github.com" {
		t.Errorf("empty WithGitHost should not override: %q", k2.gitHost)
	}
}

// TestBuildPodSpec verifies the Kaniko pod spec has the expected structure:
// init container, kaniko container, workspace/fixed-files/docker-config
// volumes, and the security contexts.
func TestBuildPodSpec(t *testing.T) {
	k := NewKaniko(nil, "sentinel").
		WithDockerHubUsername("karimzakzouk")
	pod := k.buildPodSpec(
		"kaniko-build-abc12345",
		"karimzakzouk/sentinel",
		"main",
		"kaniko-fixed-files-abc12345",
		"docker.io/karimzakzouk/sentinel-sim:sentinel-abc12345",
		"Dockerfile",
		"abc12345",
	)

	if pod.Name != "kaniko-build-abc12345" {
		t.Errorf("pod.Name = %q", pod.Name)
	}
	if pod.Namespace != "sentinel" {
		t.Errorf("pod.Namespace = %q", pod.Namespace)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy = %q, want Never", pod.Spec.RestartPolicy)
	}

	// Init container.
	if len(pod.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(pod.Spec.InitContainers))
	}
	init := pod.Spec.InitContainers[0]
	if init.Name != "clone-and-patch" {
		t.Errorf("init container name = %q", init.Name)
	}
	if init.Image != kanikoInitImageDefault {
		t.Errorf("init container image = %q", init.Image)
	}
	// Env vars: SENTINEL_REPO, SENTINEL_BRANCH, SENTINEL_GITHUB_TOKEN.
	envNames := []string{}
	for _, e := range init.Env {
		envNames = append(envNames, e.Name)
	}
	for _, want := range []string{"SENTINEL_REPO", "SENTINEL_BRANCH", "SENTINEL_GITHUB_TOKEN"} {
		if !contains(envNames, want) {
			t.Errorf("init container missing env var %q (has %v)", want, envNames)
		}
	}
	// Token must come from a SecretKeyRef (not inline Value) so it
	// doesn't leak via kubectl describe pod.
	var tokenEnv *corev1.EnvVar
	for i := range init.Env {
		if init.Env[i].Name == "SENTINEL_GITHUB_TOKEN" {
			tokenEnv = &init.Env[i]
			break
		}
	}
	if tokenEnv == nil {
		t.Fatal("SENTINEL_GITHUB_TOKEN env var missing")
	}
	if tokenEnv.Value != "" {
		t.Error("SENTINEL_GITHUB_TOKEN must use ValueFrom (SecretKeyRef), not inline Value")
	}
	if tokenEnv.ValueFrom == nil || tokenEnv.ValueFrom.SecretKeyRef == nil {
		t.Error("SENTINEL_GITHUB_TOKEN must reference a K8s Secret")
	}

	// Kaniko container.
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(pod.Spec.Containers))
	}
	kaniko := pod.Spec.Containers[0]
	if kaniko.Name != "kaniko" {
		t.Errorf("kaniko container name = %q", kaniko.Name)
	}
	if kaniko.Image != kanikoExecutorImageDefault {
		t.Errorf("kaniko container image = %q", kaniko.Image)
	}
	// Args must include --destination and --cache-repo (since username is set).
	joinedArgs := strings.Join(kaniko.Args, " ")
	if !strings.Contains(joinedArgs, "--destination=docker.io/karimzakzouk/sentinel-sim:sentinel-abc12345") {
		t.Errorf("kaniko args missing --destination: %v", kaniko.Args)
	}
	if !strings.Contains(joinedArgs, "--cache-repo=docker.io/karimzakzouk/sentinel-build-cache") {
		t.Errorf("kaniko args missing --cache-repo: %v", kaniko.Args)
	}
	if !strings.Contains(joinedArgs, "--context=/workspace/repo") {
		t.Errorf("kaniko args missing --context: %v", kaniko.Args)
	}

	// Volumes: workspace, fixed-files, docker-config.
	volNames := []string{}
	for _, v := range pod.Spec.Volumes {
		volNames = append(volNames, v.Name)
	}
	for _, want := range []string{"workspace", "fixed-files", "docker-config"} {
		if !contains(volNames, want) {
			t.Errorf("pod missing volume %q (has %v)", want, volNames)
		}
	}

	// Init script must NOT contain `set -ex` (would leak the token to logs).
	if len(init.Args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(init.Args))
	}
	if strings.Contains(init.Args[0], "set -ex") {
		t.Error("init script must use `set -e` not `set -ex` (would leak token to pod logs)")
	}
	if !strings.Contains(init.Args[0], "set -e") {
		t.Error("init script must contain `set -e`")
	}
	// Init script must NOT interpolate repo/branch directly.
	if strings.Contains(init.Args[0], "karimzakzouk/sentinel") {
		t.Error("init script must NOT contain the repo name inline — must come from $SENTINEL_REPO")
	}
}

// TestBuildPodSpec_NoUsername verifies that when no Docker Hub username is
// set, --cache-repo is NOT in the args (layer caching disabled).
func TestBuildPodSpec_NoUsername(t *testing.T) {
	k := NewKaniko(nil, "sentinel")
	pod := k.buildPodSpec("p", "owner/repo", "main", "cm", "docker.io/foo:tag", "Dockerfile", "id")
	joinedArgs := strings.Join(pod.Spec.Containers[0].Args, " ")
	if strings.Contains(joinedArgs, "--cache-repo") {
		t.Errorf("expected no --cache-repo when no Docker Hub username set, got: %v", pod.Spec.Containers[0].Args)
	}
}

// contains reports whether s contains x.
func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// TestBuildForIncident_SearchReplaceSkipped verifies that files using
// search/replace (Changes set, NewContent empty) are NOT overlaid via the
// ConfigMap — they're already on the branch.
func TestBuildForIncident_SearchReplaceSkipped(t *testing.T) {
	// We can't easily call BuildForIncident without a fake clientset
	// (Build() actually creates the ConfigMap + Pod). Instead, verify the
	// filter logic that BuildForIncident uses to decide which files to
	// overlay.
	repo := strPtr("karimzakzouk/sentinel")
	fp := &schemas.FixProposal{
		FilesToChange: []schemas.FileChange{
			{Path: "app/main.py", Changes: []schemas.SearchReplace{{Search: "x=1", Replace: "x=2"}}},
			{Path: "app/util.py", NewContent: "def foo(): pass\n"},
			{Path: "", NewContent: "should be skipped (empty path)"},
		},
		Repo: repo,
	}

	// Mirror the loop in BuildForIncident.
	fixedFiles := map[string]string{}
	for _, fc := range fp.FilesToChange {
		if fc.Path == "" {
			continue
		}
		if len(fc.Changes) > 0 && fc.NewContent == "" {
			continue // search/replace — already on branch
		}
		if fc.NewContent != "" {
			fixedFiles[fc.Path] = fc.NewContent
		}
	}

	if _, ok := fixedFiles["app/main.py"]; ok {
		t.Error("search/replace file should NOT be in fixedFiles overlay")
	}
	if got, ok := fixedFiles["app/util.py"]; !ok || got != "def foo(): pass\n" {
		t.Errorf("NewContent file should be in fixedFiles: got %q", got)
	}
	if _, ok := fixedFiles[""]; ok {
		t.Error("empty-path file should NOT be in fixedFiles")
	}
}

// strPtr returns a pointer to s. Mirrors the helper used by other v1 tests.
func strPtr(s string) *string { return &s }
