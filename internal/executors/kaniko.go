package executors

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// KanikoBuilder builds Docker images inside Kubernetes without a daemon.
// Creates a ConfigMap with the fixed files, launches a Kaniko pod that:
//   1. Clones the repo at the fix branch (init container)
//   2. Overlays the fixed files from the ConfigMap
//   3. Builds the Docker image via Kaniko
//   4. Pushes to Docker Hub
//   5. Exits
type KanikoBuilder struct {
	clientset       *kubernetes.Clientset
	namespace       string
	dockerSecretName string
}

// NewKaniko creates a KanikoBuilder.
func NewKaniko(clientset *kubernetes.Clientset, namespace string) *KanikoBuilder {
	return &KanikoBuilder{
		clientset:        clientset,
		namespace:        namespace,
		dockerSecretName: "kaniko-docker-hub-creds",
	}
}

// Build builds a Docker image with the fixed files applied.
// Returns the full image reference (e.g. "docker.io/karimzakzouk/sentinel-sim:sentinel-abc12345").
func (k *KanikoBuilder) Build(ctx context.Context, repo, branch string, fixedFiles map[string]string, imageName, imageTag, dockerfilePath string) (string, error) {
	buildID := strings.ReplaceAll(imageTag, "sentinel-", "")[:8]
	configmapName := fmt.Sprintf("kaniko-fixed-files-%s", buildID)
	podName := fmt.Sprintf("kaniko-build-%s", buildID)
	fullImage := fmt.Sprintf("docker.io/%s:%s", imageName, imageTag)

	log.Printf("[kaniko] starting build: %s (repo=%s, branch=%s)", fullImage, repo, branch)

	// 1. Create ConfigMap with the fixed files
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
	success := k.waitForCompletion(ctx, podName, 5*time.Minute)
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

func (k *KanikoBuilder) createConfigMap(ctx context.Context, name string, files map[string]string) error {
	data := map[string]string{}
	for path, content := range files {
		// Encode path: K8s ConfigMap keys can't contain '/'
		key := strings.ReplaceAll(path, "/", "__")
		data[key] = content
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: k.namespace,
			Labels: map[string]string{
				"app":                   "kaniko-build",
				"sentinel.io/build-id":  name,
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

func (k *KanikoBuilder) buildPodSpec(podName, repo, branch, configmapName, fullImage, dockerfilePath, buildID string) *corev1.Pod {
	initArgs := fmt.Sprintf(`set -ex
git clone --depth 1 --branch %s https://github.com/%s.git /workspace/repo
cd /workspace/repo
for f in /fixed-files/*; do
    key=$(basename "$f")
    rel_path=$(echo "$key" | sed 's/__/\//g')
    dest="/workspace/repo/$rel_path"
    mkdir -p "$(dirname "$dest")"
    cp "$f" "$dest"
    echo "Applied fix: $key → $dest"
done
ls -la /workspace/repo/`, branch, repo)

	kanikoArgs := []string{
		fmt.Sprintf("--dockerfile=%s", dockerfilePath),
		fmt.Sprintf("--destination=%s", fullImage),
		"--context=/workspace/repo",
		"--force",
		"--cache=false",
		"--snapshotMode=redo",
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: k.namespace,
			Labels: map[string]string{
				"app":                  "kaniko-build",
				"sentinel.io/build-id": buildID,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			InitContainers: []corev1.Container{
				{
					Name:         "clone-and-patch",
					Image:        "alpine/git:latest",
					WorkingDir:   "/workspace",
					Command:      []string{"/bin/sh", "-c"},
					Args:         []string{initArgs},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
						{Name: "fixed-files", MountPath: "/fixed-files", ReadOnly: true},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:       "kaniko",
					Image:      "gcr.io/kaniko-project/executor:v1.24.0",
					Args:       kanikoArgs,
					WorkingDir: "/workspace",
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
		if fc.Path != "" {
			fixedFiles[fc.Path] = fc.NewContent
		}
	}

	return k.Build(ctx, branchInfo.Repo, branchInfo.BranchName, fixedFiles, imageName, imageTag, dockerfilePath)
}
