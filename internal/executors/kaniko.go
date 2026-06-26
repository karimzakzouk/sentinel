package executors

import (
        "bytes"
        "context"
        "errors"
        "fmt"
        "log"
        "regexp"
        "strings"
        "time"

        corev1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/client-go/kubernetes"

        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
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
        dockerHubUsername string // v3.3.1: used for --cache-repo (was incorrectly using secret name)
        // v3.11.0: Registry-aware support (CRITICAL audit #7). Default "docker.io".
        // ECR users set "123456789.dkr.ecr.us-east-1.amazonaws.com", GCR users set
        // "gcr.io/myproject", Harbor users set "harbor.mycorp.internal", etc.
        registry string
        // v3.11.0: Configurable Kaniko images (audit #14).
        initImage     string
        executorImage string
        // v3.11.0: Configurable git host (CRITICAL audit #2). Default "github.com".
        // The Kaniko init container's git clone uses this host.
        gitHost string
}

// NewKaniko creates a KanikoBuilder.
func NewKaniko(clientset *kubernetes.Clientset, namespace string) *KanikoBuilder {
        return &KanikoBuilder{
                clientset:        clientset,
                namespace:        namespace,
                dockerSecretName: "kaniko-docker-hub-creds",
                registry:         "docker.io", // v3.11.0: was hardcoded in Build()
                initImage:        kanikoInitImageDefault,
                executorImage:    kanikoExecutorImageDefault,
                gitHost:          "github.com", // v3.11.0: was hardcoded in init script
        }
}

// WithDockerHubUsername sets the Docker Hub username used for the Kaniko
// layer cache repository. v3.3.1: This fixes a bug where the cache-repo was
// constructed as "docker.io/<secret-name>/sentinel-build-cache" (invalid path)
// instead of "docker.io/<username>/sentinel-build-cache".
func (k *KanikoBuilder) WithDockerHubUsername(username string) *KanikoBuilder {
        k.dockerHubUsername = username
        return k
}

// WithRegistry sets the container registry Kaniko pushes to. v3.11.0:
// Replaces the hardcoded "docker.io" in Build() and buildPodSpec().
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
// v3.11.0: Replaces the hardcoded const kanikoInitImage.
func (k *KanikoBuilder) WithInitImage(image string) *KanikoBuilder {
        if image != "" {
                k.initImage = image
        }
        return k
}

// WithExecutorImage sets the Kaniko executor image.
// v3.11.0: Replaces the hardcoded const kanikoExecutorImage.
func (k *KanikoBuilder) WithExecutorImage(image string) *KanikoBuilder {
        if image != "" {
                k.executorImage = image
        }
        return k
}

// WithGitHost sets the git host used in the Kaniko init container's clone URL.
// v3.11.0: Replaces the hardcoded "https://github.com/" in the init script.
// For GitHub Enterprise use "gh.mycorp.internal",
// for GitLab use "gitlab.com", etc.
func (k *KanikoBuilder) WithGitHost(host string) *KanikoBuilder {
        if host != "" {
                k.gitHost = host
        }
        return k
}

// repoBranchRe is the strict allowlist for repo and branch strings passed
// from pod annotations. Anything outside this set is rejected before it can
// reach a shell. The regex matches:
//   - repo: "owner/name" or "owner/name.git" — letters, digits, . _ - /
//   - branch: typical git branch names — letters, digits, . _ - /
//
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
//
// v3.8.4: This function previously interpolated repo and branch verbatim into
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

        buildID := strings.ReplaceAll(imageTag, "sentinel-", "")[:8]
        configmapName := fmt.Sprintf("kaniko-fixed-files-%s", buildID)
        podName := fmt.Sprintf("kaniko-build-%s", buildID)
        // v3.11.0: Use the configured registry (CRITICAL audit #7). Was:
        //   fullImage := fmt.Sprintf("docker.io/%s:%s", imageName, imageTag)
        // which broke for ECR/GCR/ACR/Harbor users — the prodImage's registry
        // was stripped and the canary was force-pushed to docker.io.
        //
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

        log.Printf("[kaniko] starting build: %s (repo=%s, branch=%s)", fullImage, repo, branch)

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
        // v3.14.2: Increased from 5min to 10min. The build itself takes ~100s,
        // but pushing the final image to Docker Hub takes 60-120s for a ~150MB
        // image. 5min was not enough — the push was killed mid-way.
        // With layer cache warm, the total drops to ~2-3min.
        success := k.waitForCompletion(ctx, podName, 10*time.Minute)
        if !success {
                // Get the logs for debugging
                logs, _ := k.getPodLogs(ctx, podName)
                log.Printf("[kaniko] build failed. Last 500 chars of logs:\n%s", lastN(logs, 500))
                k.cleanup(ctx, configmapName, podName)
                return "", fmt.Errorf("kaniko build failed for %s", fullImage)
        }

        log.Printf("[kaniko] build succeeded: %s", fullImage)
        k.cleanup(ctx, configmapName, podName)
        return fullImage, nil
}

// createConfigMap packages fixed files into a ConfigMap.
//
// v3.8.4: Previous implementation encoded file paths as
// `strings.ReplaceAll(path, "/", "__")` and the init container reversed this
// with `sed 's/__/\//g'`. That is lossy: a file path like `src/__test__/foo.py`
// would be encoded as `src____test____foo.py` and decoded back to
// `src/test/foo.py` — a silent corruption that the build would never catch.
//
// We now use a path-sidecar approach: for each fixed file at index i we
// create two ConfigMap keys:
//
//      file-i.content -> the file content (the value)
//      file-i.path    -> the real repo-relative destination path
//
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
        if err != nil && !strings.Contains(err.Error(), "already exists") {
                return err
        }
        return nil
}

// kanikoInitImageDefault is the default image used for the clone-and-patch
// init container. v3.11.0: Now overridable via WithInitImage() +
// cfg.KanikoInitImage env var (was an unexported const).
// Tag-pinned to alpine/git:2.43.0 (we avoid `:latest` so a malicious
// upstream rebuild can't change behavior under us). For maximum supply-chain
// hardening, operators should re-pin to a digest via the Sentinel config.
// alpine/git bundles git + busybox shell — enough for our pure-shell init
// script. We don't need jq because we use the path-sidecar approach (see
// createConfigMap) which is pure shell.
const kanikoInitImageDefault = "alpine/git:2.43.0"

// kanikoExecutorImageDefault is the default Kaniko executor image.
// v3.11.0: Now overridable via WithExecutorImage() + cfg.KanikoExecutorImage.
// Tag-pinned to a stable upstream release; operators should re-pin to a
// digest for prod.
const kanikoExecutorImageDefault = "gcr.io/kaniko-project/executor:v1.24.0"

// buildPodSpec builds the Kaniko build pod.
//
// v3.8.4: repo and branch are passed to the init container as env vars
// (SENTINEL_REPO, SENTINEL_BRANCH) rather than interpolated into the shell
// script. This is the second layer of defense against shell injection —
// even if validateRepoBranch were ever bypassed, the env-var path can't be
// turned into a shell metacharacter. The init script itself is fully static.
func (k *KanikoBuilder) buildPodSpec(podName, repo, branch, configmapName, fullImage, dockerfilePath, buildID string) *corev1.Pod {
        // The init container script is fully static — no string interpolation of
        // repo / branch / paths. All variable inputs come from env vars and the
        // path-sidecar files mounted from the ConfigMap.
        // v3.11.0: The git host is templated from k.gitHost (was hardcoded
        // "https://github.com/"). This is safe because k.gitHost comes from
        // cfg.GitHost, which is set by the operator at deploy time — NOT from
        // pod annotations (which would be an injection vector).
        initScript := `set -ex

# Repo and branch arrive as env vars — they cannot inject shell
# metacharacters even if a malicious value slipped past validation.
# v3.11.0: The git host is templated at pod-creation time from the
# operator-set cfg.GitHost (default "github.com"). It is NOT an env var
# because that would re-introduce the injection surface — the host is
# trusted config, repo/branch are not.
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
                "--cache=true", // v3.3.0: enable layer caching for faster builds
        }
        // v3.11.0: Use the configured registry for --cache-repo (CRITICAL audit #7).
        // Was: "--cache-repo=docker.io/"+k.dockerHubUsername+"/sentinel-build-cache"
        // which broke for ECR/GCR/ACR/Harbor users — the cache push went to
        // docker.io even when --destination was a different registry.
        if k.dockerHubUsername != "" {
                kanikoArgs = append(kanikoArgs,
                        "--cache-repo="+k.registry+"/"+k.dockerHubUsername+"/sentinel-build-cache",
                        "--cache-ttl=168h", // cache layers for 7 days
                )
        } else {
                log.Printf("[kaniko] WARNING: no Docker Hub username set — layer caching disabled (set WithDockerHubUsername)")
        }
        kanikoArgs = append(kanikoArgs, "--snapshotMode=redo")

        // v3.14.0 fix: Kaniko needs root + CAP_CHOWN to build images.
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
                                // v3.13.0: No RunAsNonRoot/RunAsUser — Kaniko needs root.
                                SeccompProfile: seccompProfile,
                        },
                        InitContainers: []corev1.Container{
                                {
                                        Name:       "clone-and-patch",
                                        Image:      k.initImage, // v3.11.0: configurable via WithInitImage
                                        WorkingDir: "/workspace",
                                        Command:    []string{"/bin/sh", "-c"},
                                        Args:       []string{initScript},
                                        Env: []corev1.EnvVar{
                                                {Name: "SENTINEL_REPO", Value: repo},
                                                {Name: "SENTINEL_BRANCH", Value: branch},
                                        },
                                        SecurityContext: &corev1.SecurityContext{
                                                AllowPrivilegeEscalation: &allowPrivilegeEscalation,
                                                ReadOnlyRootFilesystem:  newBool(true),
                                                SeccompProfile:          seccompProfile,
                                                // v3.14.0: No Capabilities drop — Kaniko needs CAP_CHOWN to extract layers
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
                                        Image:      k.executorImage, // v3.11.0: configurable via WithExecutorImage
                                        Args:       kanikoArgs,
                                        WorkingDir: "/workspace",
                                        SecurityContext: &corev1.SecurityContext{
                                                AllowPrivilegeEscalation: &allowPrivilegeEscalation,
                                                SeccompProfile:          seccompProfile,
                                                // v3.14.0: No Capabilities drop — Kaniko needs CAP_CHOWN to extract layers
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
                pod, err := k.clientset.CoreV1().Pods(k.namespace).Get(ctx, podName, metav1.GetOptions{})
                if err == nil {
                        switch pod.Status.Phase {
                        case corev1.PodSucceeded:
                                return true
                        case corev1.PodFailed:
                                return false
                        }
                }
                time.Sleep(5 * time.Second)
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
        _ = k.clientset.CoreV1().ConfigMaps(k.namespace).Delete(ctx, configmapName, metav1.DeleteOptions{})
        _ = k.clientset.CoreV1().Pods(k.namespace).Delete(ctx, podName, metav1.DeleteOptions{})
}

func lastN(s string, n int) string {
        if len(s) <= n {
                return s
        }
        return s[len(s)-n:]
}

// BuildForIncident is a convenience wrapper that extracts all params from a
// FixProposal + branch info and calls Build.
//
// v3.9.0: If a FileChange uses search/replace (Changes) instead of
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
        }

        fixedFiles := map[string]string{}
        for _, fc := range fp.FilesToChange {
                if fc.Path == "" {
                        continue
                }
                // v3.9.0: If this is a search/replace change, the file is already
                // fixed on the branch. Skip the ConfigMap overlay — Kaniko will
                // clone the branch and get the fixed file directly.
                if len(fc.Changes) > 0 && fc.NewContent == "" {
                        log.Printf("[kaniko] file %s uses search/replace — already on branch, skipping overlay", fc.Path)
                        continue
                }
                if fc.NewContent != "" {
                        fixedFiles[fc.Path] = fc.NewContent
                }
        }

        return k.Build(ctx, branchInfo.Repo, branchInfo.BranchName, fixedFiles, imageName, imageTag, dockerfilePath)
}
