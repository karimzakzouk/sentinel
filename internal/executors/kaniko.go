package executors

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/internal/errs"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// kanikoInitImageDefault is the default image used for the clone-and-patch
// init container. Tag-pinned to alpine/git:2.43.0 (no :latest so a
// malicious upstream rebuild cannot change behavior). Operators should
// re-pin to a digest for prod supply-chain hardening.
const kanikoInitImageDefault = "alpine/git:2.43.0"

// kanikoExecutorImageDefault is the default Kaniko executor image. Tag-pinned
// to a stable upstream release; operators should re-pin to a digest for prod.
const kanikoExecutorImageDefault = "gcr.io/kaniko-project/executor:v1.24.0"

// KanikoBuilder builds Docker images inside Kubernetes without a daemon.
// It creates a ConfigMap with the fixed files, launches a Kaniko pod that:
//  1. Clones the repo at the fix branch (init container).
//  2. Overlays the fixed files from the ConfigMap.
//  3. Builds the Docker image via Kaniko.
//  4. Pushes to the configured registry.
//  5. Exits.
type KanikoBuilder struct {
	clientset         kubernetes.Interface
	namespace         string
	dockerSecretName  string
	dockerHubUsername string
	registry          string
	initImage         string
	executorImage     string
	gitHost           string
	githubToken       string
}

// NewKaniko creates a KanikoBuilder with default registry (docker.io),
// init image, executor image, and git host (github.com).
func NewKaniko(clientset kubernetes.Interface, namespace string) *KanikoBuilder {
	return &KanikoBuilder{
		clientset:        clientset,
		namespace:        namespace,
		dockerSecretName: "kaniko-docker-hub-creds",
		registry:         "docker.io",
		initImage:        kanikoInitImageDefault,
		executorImage:    kanikoExecutorImageDefault,
		gitHost:          "github.com",
	}
}

// WithDockerHubUsername sets the Docker Hub username used for the Kaniko
// layer cache repository.
func (k *KanikoBuilder) WithDockerHubUsername(username string) *KanikoBuilder {
	k.dockerHubUsername = username
	return k
}

// WithRegistry sets the container registry Kaniko pushes to. For ECR use
// "<account>.dkr.ecr.<region>.amazonaws.com", for GCR use "gcr.io/<project>",
// for ACR use "<name>.azurecr.io", for Harbor use "<host>",
// for Quay use "quay.io/<org>".
func (k *KanikoBuilder) WithRegistry(registry string) *KanikoBuilder {
	if registry != "" {
		k.registry = registry
	}
	return k
}

// WithInitImage sets the Kaniko init container image (clone-and-patch).
func (k *KanikoBuilder) WithInitImage(image string) *KanikoBuilder {
	if image != "" {
		k.initImage = image
	}
	return k
}

// WithExecutorImage sets the Kaniko executor image.
func (k *KanikoBuilder) WithExecutorImage(image string) *KanikoBuilder {
	if image != "" {
		k.executorImage = image
	}
	return k
}

// WithGitHost sets the git host used in the Kaniko init container's clone
// URL. For GitHub Enterprise use "gh.mycorp.internal", for GitLab use
// "gitlab.com", etc.
func (k *KanikoBuilder) WithGitHost(host string) *KanikoBuilder {
	if host != "" {
		k.gitHost = host
	}
	return k
}

// WithGitHubToken sets the GitHub PAT used by the init container's git
// clone. The token is passed to the init container via an env var sourced
// from a K8s Secret reference (not inline), so it doesn't leak via pod
// describe/logs.
func (k *KanikoBuilder) WithGitHubToken(token string) *KanikoBuilder {
	k.githubToken = token
	return k
}

// repoBranchRe is the strict allowlist for repo and branch strings passed
// from pod annotations. Anything outside this set is rejected before it
// can reach a shell.
var repoBranchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

// ErrInvalidRepoBranch is returned when the repo or branch string from a
// pod annotation fails the strict allowlist check.
var ErrInvalidRepoBranch = errors.New("invalid repo or branch: must match ^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$")

// validateRepoBranch rejects any repo/branch string that contains shell
// metacharacters or non-path characters. This is the primary defense
// against the Kaniko init-container shell-injection vector — repo and
// branch come from pod annotations that any user with pod-edit permission
// can set. We never interpolate them into a shell script without this check.
func validateRepoBranch(repo, branch string) error {
	if !repoBranchRe.MatchString(repo) {
		return fmt.Errorf("%w: repo=%q", ErrInvalidRepoBranch, repo)
	}
	if !repoBranchRe.MatchString(branch) {
		return fmt.Errorf("%w: branch=%q", ErrInvalidRepoBranch, branch)
	}
	if strings.Contains(repo, "..") || strings.Contains(branch, "..") {
		return fmt.Errorf("%w: contains '..'", ErrInvalidRepoBranch)
	}
	return nil
}

// BuildError is a typed Kaniko build failure. Infra=true means the failure
// is environmental (registry auth, base-image pull, node mounts, network)
// and the LLM cannot fix it. Infra=false means the build broke on the fix's
// own code (compile error, bad manifest edit, missing file) — the failure
// output is actionable feedback for the fix proposer.
type BuildError struct {
	Image string
	Infra bool
	Logs  string
}

func (e *BuildError) Error() string {
	kind := "compile"
	if e.Infra {
		kind = "infrastructure"
	}
	return fmt.Sprintf("kaniko build failed (%s) for %s", kind, e.Image)
}

// compileErrorMarkers match build-stage failures caused by the fix's code:
// compiler diagnostics, dependency resolution on the fix's manifest edits,
// missing files, syntax errors. These belong to the LLM's sphere.
var compileErrorMarkers = []string{
	"error[e",             // rustc: error[E0308]
	"error:",              // rustc/go/javac/npm/mvn general
	"error]",              // maven: [ERROR]
	"undefined:",          // go: ./src/db.go:12:5: undefined: x
	"cannot find package", // go
	"build failure",       // maven
	"npm err",             // npm
	"modulenotfound",      // python/js
	"module not found",
	"cannot find module",
	"syntaxerror",
	"traceback (most recent call last)", // python in RUN steps
	"compilation terminated",            // gcc
	"no such file or directory",
	"manifest unknown",              // bad base image reference edited by a fix
	"failed to resolve",             // dockerfile FROM resolution against edited files
	"did not complete successfully", // buildkit-style step failure summary
	"exit code: 1", "exit status 1", "exit code 1",
	"unresolved dependency",
	"edition", // Cargo.toml edition problems (run #27: edition 2024 vs old toolchain)
}

// infraErrorMarkers match environmental failures the LLM cannot fix.
var infraErrorMarkers = []string{
	"unauthorized", "authentication required", "pull access denied",
	"dial tcp", "i/o timeout", "context deadline exceeded",
	"too many requests", "rate limit",
	"no space left on device",
	"unlinkat", // kind node mount quirk (run #21)
	"failed to authorize",
	"connection refused", "connection reset",
	"eof",
	"imagepullbackoff",
}

// ClassifyBuildLogs decides whether a failed Kaniko build broke on the
// fix's code (Infra=false) or on infrastructure (Infra=true). Compile
// markers win over infra markers when both appear — a compile error inside
// a layer build is still the fix's fault even if the tail also mentions a
// registry retry. Unknown logs default to infra (the safe, non-retrying
// historical behavior).
func ClassifyBuildLogs(logs string) bool {
	lower := strings.ToLower(logs)
	if lower == "" {
		return true
	}
	for _, m := range compileErrorMarkers {
		if strings.Contains(lower, m) {
			return false
		}
	}
	for _, m := range infraErrorMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return true
}

// Build builds a Docker image with the fixed files applied. Returns the
// full image reference (e.g. "docker.io/karimzakzouk/sentinel-sim:sentinel-abc12345").
// A failed build returns a *BuildError carrying the classification + logs.
func (k *KanikoBuilder) Build(ctx context.Context, repo, branch string, fixedFiles map[string]string, imageName, imageTag, dockerfilePath string) (string, error) {
	if err := validateRepoBranch(repo, branch); err != nil {
		return "", err
	}

	buildID := buildIDFromTag(imageTag)
	configmapName := fmt.Sprintf("kaniko-fixed-files-%s", buildID)
	podName := fmt.Sprintf("kaniko-build-%s", buildID)

	// If imageName already contains a "/", use it as-is — the canary goes
	// to the same registry as the prod image (so the prod namespace's
	// image pull secrets work for the canary too). Only when imageName is
	// a bare name (no "/") do we prepend k.registry.
	var fullImage string
	if strings.Contains(imageName, "/") {
		fullImage = fmt.Sprintf("%s:%s", imageName, imageTag)
	} else {
		fullImage = fmt.Sprintf("%s/%s:%s", k.registry, imageName, imageTag)
	}

	logging.Info("starting Kaniko build", "image", fullImage, "repo", repo, "branch", branch)

	if err := k.createConfigMap(ctx, configmapName, fixedFiles); err != nil {
		return "", fmt.Errorf("create configmap: %w", err)
	}

	pod := k.buildPodSpec(podName, repo, branch, configmapName, fullImage, dockerfilePath, buildID)
	if _, err := k.clientset.CoreV1().Pods(k.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		k.cleanup(ctx, configmapName, podName)
		return "", fmt.Errorf("create kaniko pod: %w", err)
	}

	success := k.waitForCompletion(ctx, podName, 10*time.Minute)
	if !success {
		logs, _ := k.getPodLogs(ctx, podName)
		// Also fetch the clone-and-patch init container logs — a failed clone
		// is infra, a successful clone followed by compile errors is not.
		cloneLogs := k.getInitContainerLogs(ctx, podName)
		fullLogs := logs
		if cloneLogs != "" {
			fullLogs = cloneLogs + "\n--- kaniko executor logs ---\n" + logs
		}
		logging.Error("kaniko build failed",
			"image", fullImage, "infra", ClassifyBuildLogs(fullLogs), "logs", lastN(fullLogs, 500))
		k.cleanup(ctx, configmapName, podName)
		return "", &BuildError{Image: fullImage, Infra: ClassifyBuildLogs(fullLogs), Logs: lastN(fullLogs, 16*1024)}
	}

	logging.Info("kaniko build succeeded", "image", fullImage)
	k.cleanup(ctx, configmapName, podName)
	return fullImage, nil
}

// createConfigMap packages fixed files into a ConfigMap using a
// path-sidecar layout: for each fixed file at index i we create two
// ConfigMap keys:
//
//	file-i.content -> the file content (the value)
//	file-i.path    -> the real repo-relative destination path
//
// This is fully lossless regardless of what characters appear in the path
// — paths live in ConfigMap *values*, which can contain any UTF-8.
// ConfigMap *keys* are restricted to [-._a-zA-Z0-9]+, which file-i.content
// and file-i.path both satisfy. No JSON parser, jq, or sed round-trip is
// needed in the init container.
func (k *KanikoBuilder) createConfigMap(ctx context.Context, name string, files map[string]string) error {
	data := map[string]string{}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	// Deterministic insertion sort for reproducible ConfigMaps (file count
	// is small in practice — <20).
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0 && paths[j] < paths[j-1]; j-- {
			paths[j], paths[j-1] = paths[j-1], paths[j]
		}
	}

	for i, p := range paths {
		data[fmt.Sprintf("file-%d.content", i)] = files[p]
		data[fmt.Sprintf("file-%d.path", i)] = p
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: k.namespace,
			Labels: map[string]string{
				"app":                  "kaniko-build",
				"sentinel.io/build-id": name,
			},
		},
		Data: data,
	}
	_, err := k.clientset.CoreV1().ConfigMaps(k.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil && !errs.IsAlreadyExists(err) {
		return fmt.Errorf("create configmap %s: %w", name, err)
	}
	return nil
}

// buildPodSpec builds the Kaniko build pod. repo and branch are passed
// to the init container as env vars (never interpolated into the shell
// script body), so even if validateRepoBranch were ever bypassed the
// env-var path cannot be turned into a shell metacharacter.
func (k *KanikoBuilder) buildPodSpec(podName, repo, branch, configmapName, fullImage, dockerfilePath, buildID string) *corev1.Pod {
	// SECURITY: use `set -e` WITHOUT `-x`. `set -ex` would print the
	// GitHub PAT (expanded) to pod logs — anyone with kubectl logs access
	// in the Sentinel namespace could read it.
	initScript := `set -e

# Repo and branch arrive as env vars — they cannot inject shell
# metacharacters even if a malicious value slipped past validation.
# The git host is templated at pod-creation time from the operator-set
# cfg.GitHost (default "github.com"). It is NOT an env var because that
# would re-introduce the injection surface — the host is trusted config,
# repo/branch are not.
if [ -n "$SENTINEL_GITHUB_TOKEN" ]; then
  git config --global credential.helper store
  printf '%s\n' "https://x-access-token:${SENTINEL_GITHUB_TOKEN}@` + k.gitHost + `" > ~/.git-credentials
  chmod 600 ~/.git-credentials
fi
git clone --depth 1 --branch "$SENTINEL_BRANCH" "https://` + k.gitHost + `/$SENTINEL_REPO.git" /workspace/repo
cd /workspace/repo

# Iterate over every path-sidecar file. Each file-N.path contains the
# real repo-relative destination path; file-N.content is the bytes to write.
for pf in /fixed-files/file-*.path; do
    [ -e "$pf" ] || continue
    base=${pf%.path}
    rel_path=$(cat "$pf")
    dest="/workspace/repo/$rel_path"
    mkdir -p "$(dirname "$dest")"
    cp "${base}.content" "$dest"
    echo "Applied fix: $(basename "$base") -> $dest"
done

ls -la /workspace/repo/`

	kanikoArgs := []string{
		fmt.Sprintf("--dockerfile=%s", dockerfilePath),
		fmt.Sprintf("--destination=%s", fullImage),
		"--context=/workspace/repo",
		"--force",
		"--cache=true",
	}
	if k.dockerHubUsername != "" {
		kanikoArgs = append(kanikoArgs,
			"--cache-repo="+k.registry+"/"+k.dockerHubUsername+"/sentinel-build-cache",
			"--cache-ttl=168h",
		)
	} else {
		logging.Warn("no Docker Hub username set — layer caching disabled (set WithDockerHubUsername)")
	}
	kanikoArgs = append(kanikoArgs, "--snapshotMode=redo")

	// Kaniko extracts the base image by DELETING its own root
	// filesystem first. Paths that are bind-mounted into the pod
	// cannot be unlinked — the build dies late (after the cache
	// push) with "unlinkat //product_uuid: device or resource
	// busy". Seen on kind nodes, where the node container carries
	// the runner host's /product_uuid mount into every pod: eval
	// run #21 lost ALL THREE sandbox canary builds this way, so
	// sandbox_verified stayed false and auto-merge gate 3 never
	// opened. --ignore-path excludes a path from both the delete
	// phase and the layer snapshot. The projected serviceaccount
	// token is included as defense-in-depth: ignoring it is also
	// the security-correct behavior — a runtime mount must never
	// leak into an image layer or the layer cache.
	kanikoArgs = append(kanikoArgs,
		"--ignore-path=/product_uuid",
		"--ignore-path=/var/run/secrets/kubernetes.io/serviceaccount",
	)

	allowPrivilegeEscalation := false
	seccompProfile := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: k.namespace,
			Labels: map[string]string{
				"app":                  "kaniko-build",
				"sentinel.io/build-id": buildID,
				"sentinel.io/managed":  "true",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				SeccompProfile: seccompProfile,
			},
			InitContainers: []corev1.Container{
				{
					Name:       "clone-and-patch",
					Image:      k.initImage,
					WorkingDir: "/workspace",
					Command:    []string{"/bin/sh", "-c"},
					Args:       []string{initScript},
					Env: []corev1.EnvVar{
						{Name: "SENTINEL_REPO", Value: repo},
						{Name: "SENTINEL_BRANCH", Value: branch},
						// EnvVarSource (not inline Value) so the token
						// doesn't appear in `kubectl describe pod`.
						{Name: "SENTINEL_GITHUB_TOKEN", ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "sentinel"},
								Key:                  "githubToken",
								Optional:             newBool(true),
							},
						}},
					},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						ReadOnlyRootFilesystem:   newBool(true),
						SeccompProfile:           seccompProfile,
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
						{Name: "fixed-files", MountPath: "/fixed-files", ReadOnly: true},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:       "kaniko",
					Image:      k.executorImage,
					Args:       kanikoArgs,
					WorkingDir: "/workspace",
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						SeccompProfile:           seccompProfile,
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
						{Name: "docker-config", MountPath: "/kaniko/.docker", ReadOnly: true},
					},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "fixed-files", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configmapName}}}},
				{Name: "docker-config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName: k.dockerSecretName,
					Items:      []corev1.KeyToPath{{Key: ".dockerconfigjson", Path: "config.json"}},
				}}},
			},
		},
	}
}

// newBool returns a pointer to b. Helper for *bool fields in corev1.
func newBool(b bool) *bool { return &b }

// waitForCompletion polls the pod until it reaches Succeeded/Failed or
// the timeout elapses.
func (k *KanikoBuilder) waitForCompletion(ctx context.Context, podName string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		pod, err := k.clientset.CoreV1().Pods(k.namespace).Get(ctx, podName, metav1.GetOptions{})
		if err == nil {
			switch pod.Status.Phase {
			case corev1.PodSucceeded:
				return true
			case corev1.PodFailed:
				return false
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Second):
		}
	}
	return false
}

// getPodLogs fetches the kaniko container's logs for debugging.
func (k *KanikoBuilder) getPodLogs(ctx context.Context, podName string) (string, error) {
	req := k.clientset.CoreV1().Pods(k.namespace).GetLogs(podName, &corev1.PodLogOptions{Container: "kaniko"})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(stream); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// getInitContainerLogs fetches the clone-and-patch init container's logs
// (best effort — used to classify build failures).
func (k *KanikoBuilder) getInitContainerLogs(ctx context.Context, podName string) string {
	req := k.clientset.CoreV1().Pods(k.namespace).GetLogs(podName, &corev1.PodLogOptions{Container: "clone-and-patch"})
	stream, err := req.Stream(ctx)
	if err != nil {
		return ""
	}
	defer stream.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(stream)
	return buf.String()
}

// cleanup deletes the build ConfigMap and Pod. Uses a fresh context so
// cleanup survives cancellation of the parent ctx — otherwise leaked
// Kaniko pods accumulate forever.
func (k *KanikoBuilder) cleanup(ctx context.Context, configmapName, podName string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = k.clientset.CoreV1().ConfigMaps(k.namespace).Delete(cleanupCtx, configmapName, metav1.DeleteOptions{})
	_ = k.clientset.CoreV1().Pods(k.namespace).Delete(cleanupCtx, podName, metav1.DeleteOptions{})
	_ = ctx
}

// lastN returns the last n bytes of s (or s itself if shorter).
func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// buildIDFromTag extracts an 8-char build ID from an image tag.
func buildIDFromTag(imageTag string) string {
	s := strings.TrimPrefix(imageTag, "sentinel-")
	if len(s) >= 8 {
		return s[:8]
	}
	if s == "" {
		return "build000"
	}
	return fmt.Sprintf("%-8s", s)[:8]
}

// validateDockerfilePath validates that a dockerfile_path from LLM output
// is safe. Allowed: "Dockerfile", "Dockerfile.*", "docker/Dockerfile",
// "subdir/Dockerfile". Rejected: absolute paths, "..", control chars,
// non-Dockerfile filenames. Prevents path traversal and build-chain
// attacks via LLM output.
func validateDockerfilePath(path string) error {
	if path == "" {
		return nil
	}
	if len(path) > 200 {
		return fmt.Errorf("path too long (%d chars)", len(path))
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("control character in path")
		}
	}
	if filepathIsAbs(path) {
		return fmt.Errorf("absolute paths not allowed")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("path traversal (..) not allowed")
	}
	base := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		base = path[idx+1:]
	}
	if base != "Dockerfile" && !strings.HasPrefix(base, "Dockerfile.") {
		return fmt.Errorf("dockerfile path must end with 'Dockerfile' or 'Dockerfile.<variant>', got %q", base)
	}
	return nil
}

// filepathIsAbs is a minimal implementation to avoid importing path/filepath
// for a single use.
func filepathIsAbs(path string) bool {
	return strings.HasPrefix(path, "/") || (len(path) >= 2 && path[1] == ':')
}

// BuildForIncident is a convenience wrapper that extracts all params from
// a FixProposal + branch info and calls Build. For files using
// search/replace, the fixed file is already on the branch (PrepareBranch
// applied the search/replace); we skip the ConfigMap overlay for those —
// Kaniko clones the branch which already has the fix. Only files with
// explicit NewContent (legacy whole-file replacement) are overlaid.
func (k *KanikoBuilder) BuildForIncident(ctx context.Context, inc *schemas.Incident, branchInfo BranchInfo, prodImage string) (string, error) {
	fp := inc.State.FixProposal
	imageName := strings.SplitN(prodImage, ":", 2)[0]
	imageTag := fmt.Sprintf("sentinel-%s", fp.FixID.String()[:8])
	dockerfilePath := "Dockerfile"
	if fp.DockerfilePath != nil && *fp.DockerfilePath != "" {
		dockerfilePath = *fp.DockerfilePath
		if err := validateDockerfilePath(dockerfilePath); err != nil {
			return "", fmt.Errorf("invalid dockerfile_path: %w", err)
		}
	}

	fixedFiles := map[string]string{}
	for _, fc := range fp.FilesToChange {
		if fc.Path == "" {
			continue
		}
		if len(fc.Changes) > 0 && fc.NewContent == "" {
			logging.Info("file uses search/replace — already on branch, skipping overlay", "path", fc.Path)
			continue
		}
		if fc.NewContent != "" {
			fixedFiles[fc.Path] = fc.NewContent
		}
	}

	return k.Build(ctx, branchInfo.Repo, branchInfo.BranchName, fixedFiles, imageName, imageTag, dockerfilePath)
}
