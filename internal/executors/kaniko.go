package executors

import (
        "github.com/karimzakzouk/sentinel/internal/logging"
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
        "github.com/karimzakzouk/sentinel/internal/schemas"
)

// KanikoBuilder builds Docker images inside Kubernetes without a daemon.
// Creates a ConfigMap with the fixed files, launches a Kaniko pod that:
//  1. Clones the repo at the fix branch (init container)
//  2. Overlays the fixed files from the ConfigMap
//  3. Builds the Docker image via Kaniko
//  4. Pushes to the configured registry (docker.io by default, ECR/GCR/ACR/Harbor supported)
//  5. Exits

type KanikoBuilder struct {
        clientset         *kubernetes.Clientset
        namespace         string
        dockerSecretName  string
        dockerHubUsername string

        // ECR users set "123456789.dkr.ecr.us-east-1.amazonaws.com", GCR users set
        // "gcr.io/myproject", Harbor users set "harbor.mycorp.internal", etc.

        registry string

        initImage     string
        executorImage string

        // The Kaniko init container's git clone uses this host.

        gitHost string

        // private repos. Was empty (so private repos failed with "authentication
        // required"). The token is passed via an env var (never interpolated
        // into the shell script body) sourced from a K8s Secret, so it doesn't
        // leak via pod logs.

        githubToken string
}

// NewKaniko creates a KanikoBuilder.

func NewKaniko(clientset *kubernetes.Clientset, namespace string) *KanikoBuilder {
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
// layer cache repository.This fixes a bug where the cache-repo was
// constructed as "docker.io/<secret-name>/sentinel-build-cache" (invalid path)
// instead of "docker.io/<username>/sentinel-build-cache".

func (k *KanikoBuilder) WithDockerHubUsername(username string) *KanikoBuilder {
        k.dockerHubUsername = username
        return k
}

// WithRegistry sets the container registry Kaniko pushes to.
// For ECR use "<account>.dkr.ecr.<region>.amazonaws.com",
// for GCR use "gcr.io/<project>",
// for ACR use "<name>.azurecr.io",
// for Harbor use "<host>",
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

// WithGitHost sets the git host used in the Kaniko init container's clone URL.
// For GitHub Enterprise use "gh.mycorp.internal",
// for GitLab use "gitlab.com", etc.

func (k *KanikoBuilder) WithGitHost(host string) *KanikoBuilder {
        if host != "" {
                k.gitHost = host
        }
        return k
}

// WithGitHubToken sets the GitHub PAT used by the init container's git clone.
// The token is passed to the init container via an env var sourced from a
// K8s Secret reference (not inline), so it doesn't leak via pod describe/logs.

func (k *KanikoBuilder) WithGitHubToken(token string) *KanikoBuilder {
        k.githubToken = token
        return k
}

// repoBranchRe is the strict allowlist for repo and branch strings passed
// from pod annotations. Anything outside this set is rejected before it can
// reach a shell. The regex matches:
//   - repo: "owner/name" or "owner/name.git" — letters, digits, . _ - /
//   - branch: typical git branch names — letters, digits, . _ - /
// It explicitly rejects shell metacharacters: ; & | $ ` ( ) < > \ " ' space
// newline * ? ~ ! { } and any control character. The {1,100} length bound
// prevents pathological inputs.

var repoBranchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

// ErrInvalidRepoBranch is returned when the repo or branch string from a pod
// annotation fails the strict allowlist check. Callers must treat this as a
// security-relevant rejection and never fall back to a looser parse.

var ErrInvalidRepoBranch = errors.New("invalid repo or branch: must match ^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$")

// validateRepoBranch rejects any repo/branch string that contains shell
// metacharacters or non-path characters. This is the primary defense against
// the Kaniko init-container shell-injection vector — repo and branch come
// from the sentinel.io/repo and sentinel.io/branch pod annotations, which
// any user with pod-edit permission can set. We must never interpolate them
// into a shell script without this check.

func validateRepoBranch(repo, branch string) error {
        if !repoBranchRe.MatchString(repo) {
                return fmt.Errorf("%w: repo=%q", ErrInvalidRepoBranch, repo)
        }
        if !repoBranchRe.MatchString(branch) {
                return fmt.Errorf("%w: branch=%q", ErrInvalidRepoBranch, branch)
        }
        // Reject parent-directory segments just in case — git refs don't need them.

        if strings.Contains(repo, "..") || strings.Contains(branch, "..") {
                return fmt.Errorf("%w: contains '..'", ErrInvalidRepoBranch)
        }
        return nil
}

// Build builds a Docker image with the fixed files applied.
// Returns the full image reference (e.g. "docker.io/karimzakzouk/sentinel-sim:sentinel-abc12345").
// a /bin/sh -c script run inside the clone-and-patch init container, allowing
// shell injection via pod annotations. repo and branch are now (a) validated
// against a strict allowlist and (b) passed to the init container as env vars,
// never as part of the shell script body. The fixed-files ConfigMap key
// encoding has also been replaced with a JSON sidecar manifest (see
// createConfigMap) so file paths containing '__' are no longer corrupted.

func (k *KanikoBuilder) Build(ctx context.Context, repo, branch string, fixedFiles map[string]string, imageName, imageTag, dockerfilePath string) (string, error) {
        if err := validateRepoBranch(repo, branch); err != nil {
                return "", err
        }

        buildID := buildIDFromTag(imageTag)
        configmapName := fmt.Sprintf("kaniko-fixed-files-%s", buildID)
        podName := fmt.Sprintf("kaniko-build-%s", buildID)

        //   fullImage := fmt.Sprintf("docker.io/%s:%s", imageName, imageTag)
        // which broke for ECR/GCR/ACR/Harbor users — the prodImage's registry
        // was stripped and the canary was force-pushed to docker.io.
        // imageName from BuildForIncident retains the prod image's registry
        // (e.g. "1234.dkr.ecr.us-east-1.amazonaws.com/myapp"). If imageName
        // already contains a "/", we use it as-is — the canary goes to the
        // same registry as the prod image (so the prod namespace's image
        // pull secrets work for the canary too). Only when imageName is a
        // bare name (no "/") do we prepend k.registry.

        var fullImage string
        if strings.Contains(imageName, "/") {
                fullImage = fmt.Sprintf("%s:%s", imageName, imageTag)
        } else {
                fullImage = fmt.Sprintf("%s/%s:%s", k.registry, imageName, imageTag)
        }

        logging.Info("starting Kaniko build", "value", fullImage, repo, branch)

        // 1. Create ConfigMap with the fixed files + manifest

        if err := k.createConfigMap(ctx, configmapName, fixedFiles); err != nil {
                return "", fmt.Errorf("create configmap: %w", err)
        }

        // 2. Create the Kaniko pod

        pod := k.buildPodSpec(podName, repo, branch, configmapName, fullImage, dockerfilePath, buildID)
        if _, err := k.clientset.CoreV1().Pods(k.namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
                k.cleanup(ctx, configmapName, podName)
                return "", fmt.Errorf("create kaniko pod: %w", err)
        }

        // 3. Wait for completion (5 min timeout)
        // but pushing the final image to Docker Hub takes 60-120s for a ~150MB
        // image. 5min was not enough — the push was killed mid-way.
        // With layer cache warm, the total drops to ~2-3min.

        success := k.waitForCompletion(ctx, podName, 10*time.Minute)
        if !success {
                // Get the logs for debugging

                logs, _ := k.getPodLogs(ctx, podName)
                logging.Error("build failed. Last 500 chars of logs:\n", "value", lastN(logs, 500))
                k.cleanup(ctx, configmapName, podName)
                return "", fmt.Errorf("kaniko build failed for %s", fullImage)
        }

        logging.Info("build succeeded", "fullimage", fullImage)
        k.cleanup(ctx, configmapName, podName)
        return fullImage, nil
}

// createConfigMap packages fixed files into a ConfigMap.
// `strings.ReplaceAll(path, "/", "__")` and the init container reversed this
// with `sed 's/__/\//g'`. That is lossy: a file path like `src/__test__/foo.py`
// would be encoded as `src____test____foo.py` and decoded back to
// `src/test/foo.py` — a silent corruption that the build would never catch.
// We now use a path-sidecar approach: for each fixed file at index i we
// create two ConfigMap keys:
//      file-i.content -> the file content (the value)
//      file-i.path    -> the real repo-relative destination path
// The init container iterates over every `file-*.path` key, reads the path
// value, and copies `file-*.content` to the destination. This is fully
// lossless regardless of what characters appear in the path — paths live in
// ConfigMap *values*, which can contain any UTF-8. ConfigMap *keys* are
// restricted to [-._a-zA-Z0-9]+, which `file-i.content` and `file-i.path`
// both satisfy. No JSON parser, jq, or sed round-trip is needed in the
// init container.

func (k *KanikoBuilder) createConfigMap(ctx context.Context, name string, files map[string]string) error {
        data := map[string]string{}

        // Deterministic order so the ConfigMap is reproducible across runs.

        paths := make([]string, 0, len(files))
        for p := range files {
                paths = append(paths, p)
        }
        // Sort for determinism. We use a tiny insertion sort to avoid importing
        // sort here — the file count is small (<20 in practice).

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
                return err
        }
        return nil
}

// kanikoInitImageDefault is the default image used for the clone-and-patch
// init container.Now overridable via WithInitImage() +
// cfg.KanikoInitImage env var (was an unexported const).
// Tag-pinned to alpine/git:2.43.0 (we avoid `:latest` so a malicious
// upstream rebuild can't change behavior under us). For maximum supply-chain
// hardening, operators should re-pin to a digest via the Sentinel config.
// alpine/git bundles git + busybox shell — enough for our pure-shell init
// script. We don't need jq because we use the path-sidecar approach (see
// createConfigMap) which is pure shell.

const kanikoInitImageDefault = "alpine/git:2.43.0"

// kanikoExecutorImageDefault is the default Kaniko executor image.
// Tag-pinned to a stable upstream release; operators should re-pin to a
// digest for prod.

const kanikoExecutorImageDefault = "gcr.io/kaniko-project/executor:v1.24.0"

// buildPodSpec builds the Kaniko build pod.
// (SENTINEL_REPO, SENTINEL_BRANCH) rather than interpolated into the shell
// script. This is the second layer of defense against shell injection —
// even if validateRepoBranch were ever bypassed, the env-var path can't be
// turned into a shell metacharacter. The init script itself is fully static.

func (k *KanikoBuilder) buildPodSpec(podName, repo, branch, configmapName, fullImage, dockerfilePath, buildID string) *corev1.Pod {
        // The init container script is fully static — no string interpolation of
        // repo / branch / paths. All variable inputs come from env vars and the
        // path-sidecar files mounted from the ConfigMap.
        // "https://github.com/"). This is safe because k.gitHost comes from
        // cfg.GitHost, which is set by the operator at deploy time — NOT from
        // pod annotations (which would be an injection vector).

        // SECURITY (C1 fix): Use `set -e` WITHOUT `-x`. The previous `set -ex`
        // caused the GitHub PAT to be leaked to pod logs — bash prints every
        // command with variables expanded before executing it, so the
        // `echo "https://x-access-token:${SENTINEL_GITHUB_TOKEN}@..."` line
        // appeared in `kubectl logs` output with the token fully expanded.
        // Anyone with kubectl logs access in the Sentinel namespace could
        // read the PAT.
        initScript := `set -e

# Repo and branch arrive as env vars — they cannot inject shell
# metacharacters even if a malicious value slipped past validation.
# The git host is templated at pod-creation time from the
# operator-set cfg.GitHost (default "github.com"). It is NOT an env var
# because that would re-introduce the injection surface — the host is
# trusted config, repo/branch are not.
# If SENTINEL_GITHUB_TOKEN is set, configure git to use it via
# the credential helper (instead of embedding it in the URL, which would
# leak via pod logs). Empty token = anonymous clone (public repos only).
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

        // which broke for ECR/GCR/ACR/Harbor users — the cache push went to
        // docker.io even when --destination was a different registry.

        if k.dockerHubUsername != "" {
                kanikoArgs = append(kanikoArgs,
                        "--cache-repo="+k.registry+"/"+k.dockerHubUsername+"/sentinel-build-cache",
                        "--cache-ttl=168h", // cache layers for 7 days
                )
        } else {
                logging.Warn("no Docker Hub username set — layer caching disabled (set WithDockerHubUsername)")
        }
        kanikoArgs = append(kanikoArgs, "--snapshotMode=redo")

        // Previous fix removed RunAsNonRoot/RunAsUser but kept Capabilities:Drop ALL,
        // which removes CAP_CHOWN → "chown /etc/gshadow: operation not permitted".
        // Kaniko is a trusted build tool — don't drop capabilities.

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

                                                // We use EnvVarSource (not inline Value) so the token doesn't appear
                                                // in `kubectl describe pod` output. The secret name is fixed
                                                // "sentinel-secrets" (created by Helm) and the key is "githubToken".

                                                {Name: "SENTINEL_GITHUB_TOKEN", ValueFrom: &corev1.EnvVarSource{
                                                        SecretKeyRef: &corev1.SecretKeySelector{
                                                                LocalObjectReference: corev1.LocalObjectReference{Name: "sentinel"},
                                                                Key:                  "githubToken",
                                                                Optional:             newBool(true), // optional so public repos still work without the secret
                                                        },
                                                }},
                                        },
                                        SecurityContext: &corev1.SecurityContext{
                                                AllowPrivilegeEscalation: &allowPrivilegeEscalation,
                                                ReadOnlyRootFilesystem:  newBool(true),
                                                SeccompProfile:          seccompProfile,
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
                                                SeccompProfile:          seccompProfile,
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

func (k *KanikoBuilder) cleanup(ctx context.Context, configmapName, podName string) {

        // after the parent ctx is cancelled (e.g. on investigation timeout).
        // Without this, leaked Kaniko pods accumulate forever (no reaper exists).

        cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer cancel()
        _ = k.clientset.CoreV1().ConfigMaps(k.namespace).Delete(cleanupCtx, configmapName, metav1.DeleteOptions{})
        _ = k.clientset.CoreV1().Pods(k.namespace).Delete(cleanupCtx, podName, metav1.DeleteOptions{})
        _ = ctx // parent ctx accepted for API compat but not used
}

func lastN(s string, n int) string {
        if len(s) <= n {
                return s
        }
        return s[len(s)-n:]
}

// buildIDFromTag extracts an 8-char build ID from an image tag.
// panicked with slice-out-of-range if the tag was shorter than 8 chars or
// didn't contain "sentinel-". Falls back to a hash of the tag.

func buildIDFromTag(imageTag string) string {
        s := strings.TrimPrefix(imageTag, "sentinel-")
        if len(s) >= 8 {
                return s[:8]
        }
        // Pad or hash short tags

        if s == "" {
                return "build000"
        }
        // Use the tag as-is if non-empty but short

        return fmt.Sprintf("%-8s", s)[:8]
}

// validateDockerfilePath validates that a dockerfile_path from LLM output is safe.
// path traversal (e.g. "../../vendor/attacker/Dockerfile") and build-chain attacks.
// Allowed: "Dockerfile", "Dockerfile.*", "docker/Dockerfile", "subdir/Dockerfile".
// Rejected: absolute paths, "..", control chars, non-Dockerfile filenames.

func validateDockerfilePath(path string) error {
        if path == "" {
                return nil // empty = use default "Dockerfile"
        }
        if len(path) > 200 {
                return fmt.Errorf("path too long (%d chars)", len(path))
        }
        // Reject control chars

        for _, r := range path {
                if r < 0x20 || r == 0x7f {
                        return fmt.Errorf("control character in path")
                }
        }
        // Reject absolute paths

        if filepathIsAbs(path) {
                return fmt.Errorf("absolute paths not allowed")
        }
        // Reject path traversal

        if strings.Contains(path, "..") {
                return fmt.Errorf("path traversal (..) not allowed")
        }
        // The basename must be "Dockerfile" or start with "Dockerfile."
        // This prevents LLM from pointing Kaniko at an arbitrary file like
        // "backdoor-Dockerfile" that it created via files_to_change.

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
// (which would add an import for a single use).

func filepathIsAbs(path string) bool {
        return strings.HasPrefix(path, "/") || (len(path) >= 2 && path[1] == ':')
}

// BuildForIncident is a convenience wrapper that extracts all params from a
// FixProposal + branch info and calls Build.
// NewContent, the fixed file is already on the branch (PrepareBranch applied
// the search/replace). In that case, we don't need to overlay it via ConfigMap
// — Kaniko clones the branch which already has the fix. We only overlay files
// that have explicit NewContent (legacy whole-file replacement).

func (k *KanikoBuilder) BuildForIncident(ctx context.Context, inc *schemas.Incident, branchInfo BranchInfo, prodImage string) (string, error) {
        fp := inc.State.FixProposal
        imageName := strings.SplitN(prodImage, ":", 2)[0]
        imageTag := fmt.Sprintf("sentinel-%s", fp.FixID.String()[:8])
        dockerfilePath := "Dockerfile"
        if fp.DockerfilePath != nil && *fp.DockerfilePath != "" {
                dockerfilePath = *fp.DockerfilePath

                // --dockerfile flag, allowing path traversal / build-chain attacks via LLM output).

                if err := validateDockerfilePath(dockerfilePath); err != nil {
                        return "", fmt.Errorf("invalid dockerfile_path: %w", err)
                }
        }

        fixedFiles := map[string]string{}
        for _, fc := range fp.FilesToChange {
                if fc.Path == "" {
                        continue
                }

                // fixed on the branch. Skip the ConfigMap overlay — Kaniko will
                // clone the branch and get the fixed file directly.

                if len(fc.Changes) > 0 && fc.NewContent == "" {
                        logging.Info("file  uses search/replace — already on branch, skipping overlay", "path", fc.Path)
                        continue
                }
                if fc.NewContent != "" {
                        fixedFiles[fc.Path] = fc.NewContent
                }
        }

        return k.Build(ctx, branchInfo.Repo, branchInfo.BranchName, fixedFiles, imageName, imageTag, dockerfilePath)
}
