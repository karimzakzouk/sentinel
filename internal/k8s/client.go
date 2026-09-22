// Package k8s wraps the Kubernetes clientset with the helper functions
// Sentinel needs: workload-annotation lookup, pod-log fetching, and CRD
// status patching.
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var fileRePre = regexp.MustCompile(`[a-zA-Z0-9_./-]+\.[a-zA-Z]{1,5}`)

// SentinelAnnotationKeys is the set of sentinel.io/* annotations we read
// from workloads.
var sentinelAnnotationKeys = map[string]bool{
	"sentinel.io/repo":           true,
	"sentinel.io/branch":         true,
	"sentinel.io/git-commit":     true,
	"sentinel.io/runbook":        true,
	"sentinel.io/team":           true,
	"sentinel.io/ignore":         true,
	"sentinel.io/owner":          true,
	"sentinel.io/criticality":    true,
	"sentinel.io/auto-merge":     true,
	"sentinel.io/admin-team":     true,
	"sentinel.io/sandbox-strict": true,
}

// Client wraps the Kubernetes clientset + dynamic client.
type Client struct {
	clientset *kubernetes.Clientset
	dynamic   dynamic.Interface
	config    *rest.Config
	noop      bool
}

// New returns a Client using in-cluster config, with fallback to the local
// kubeconfig (KUBECONFIG or ~/.kube/config) for dev/testing. Returns an
// error if neither works.
func New() (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		overrides := &clientcmd.ConfigOverrides{}
		clientCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)
		fallback, fbErr := clientCfg.ClientConfig()
		if fbErr != nil {
			return nil, fmt.Errorf("in-cluster config: %w; kubeconfig fallback: %v", err, fbErr)
		}
		cfg = fallback
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}
	return &Client{clientset: clientset, dynamic: dyn, config: cfg}, nil
}

// NewNoop returns a Client that never talks to a Kubernetes cluster. All
// methods return empty results and nil errors. Used when no K8s cluster is
// available (auto-degrade) or in observe-only mode.
func NewNoop() *Client {
	return &Client{noop: true}
}

// IsNoop reports whether the client is in noop mode (no real K8s cluster).
func (c *Client) IsNoop() bool {
	return c != nil && c.noop
}

// Clientset returns the underlying kubernetes clientset.
func (c *Client) Clientset() *kubernetes.Clientset { return c.clientset }

// DynamicClient returns the dynamic interface (for CRD operations).
func (c *Client) DynamicClient() dynamic.Interface { return c.dynamic }

// LookupWorkloadAnnotations finds sentinel.io/* annotations on the workload
// matching service in namespace. Tries Deployment by name → by label
// app.kubernetes.io/name=service → by label app=service → StatefulSet by
// name → Pod by label. Returns an empty map if nothing is found.
func (c *Client) LookupWorkloadAnnotations(ctx context.Context, service, namespace string) (map[string]string, error) {
	if c.IsNoop() {
		return map[string]string{}, nil
	}
	annotations := map[string]string{}

	if dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, service, metav1.GetOptions{}); err == nil {
		mergeAnnotations(annotations, dep.Annotations, dep.Spec.Template.Annotations)
		if len(annotations) > 0 {
			return annotations, nil
		}
	}

	if deps, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
	}); err == nil && len(deps.Items) > 0 {
		dep := &deps.Items[0]
		mergeAnnotations(annotations, dep.Annotations, dep.Spec.Template.Annotations)
		if len(annotations) > 0 {
			return annotations, nil
		}
	}

	if deps, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", service),
	}); err == nil && len(deps.Items) > 0 {
		dep := &deps.Items[0]
		mergeAnnotations(annotations, dep.Annotations, dep.Spec.Template.Annotations)
		if len(annotations) > 0 {
			return annotations, nil
		}
	}

	if sts, err := c.clientset.AppsV1().StatefulSets(namespace).Get(ctx, service, metav1.GetOptions{}); err == nil {
		mergeAnnotations(annotations, sts.Annotations, sts.Spec.Template.Annotations)
		if len(annotations) > 0 {
			return annotations, nil
		}
	}

	if pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
	}); err == nil && len(pods.Items) > 0 {
		mergeAnnotations(annotations, pods.Items[0].Annotations, nil)
		if len(annotations) > 0 {
			return annotations, nil
		}
	}

	return annotations, nil
}

// FetchCrashingPodLogs returns up to 8KB of logs from the worst-crashing pod
// matching service in namespace. "Worst" means CrashLoopBackOff first,
// otherwise the pod with the most restarts. Tries previous-container logs
// (the crash output) first, then current logs. Retries up to 3 times with
// 2-second delays because K8s sometimes returns "unable to retrieve
// container logs for containerd://..." immediately after a restart.
//
// Returns ("", nil) if no matching pod is found or no valid log content
// could be retrieved.
func (c *Client) FetchCrashingPodLogs(ctx context.Context, service, namespace string) (string, error) {
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
	})
	if err != nil || len(pods.Items) == 0 {
		pods, err = c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", service),
		})
		if err != nil || len(pods.Items) == 0 {
			return "", nil
		}
	}

	var worstPod *corev1.Pod
	worstRestarts := -1
	for i := range pods.Items {
		pod := &pods.Items[i]
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				worstPod = pod
				break
			}
		}
		if worstPod != nil {
			break
		}
		restarts := 0
		for _, cs := range pod.Status.ContainerStatuses {
			restarts += int(cs.RestartCount)
		}
		if restarts > worstRestarts {
			worstRestarts = restarts
			worstPod = pod
		}
	}
	if worstPod == nil {
		return "", nil
	}

	limitBytes := int64(8192)
	for attempt := range 3 {
		opts := &corev1.PodLogOptions{Previous: true, LimitBytes: &limitBytes}
		if logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(worstPod.Name, opts).Do(ctx).Raw(); err == nil {
			if isValidLogContent(string(logs)) {
				return string(logs), nil
			}
		}
		opts.Previous = false
		if logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(worstPod.Name, opts).Do(ctx).Raw(); err == nil {
			if isValidLogContent(string(logs)) {
				return string(logs), nil
			}
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return "", nil
}

// isValidLogContent reports whether s is non-empty and not just the K8s
// "unable to retrieve container logs for containerd://..." placeholder.
func isValidLogContent(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	if !strings.Contains(s, "unable to retrieve container logs for containerd://") {
		return true
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "unable to retrieve container logs") {
			continue
		}
		return true
	}
	return false
}

// GetProdImage returns the current image of the first container on the
// Deployment matching service in namespace.
func (c *Client) GetProdImage(ctx context.Context, service, namespace string) (string, error) {
	if deps, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
	}); err == nil && len(deps.Items) > 0 {
		containers := deps.Items[0].Spec.Template.Spec.Containers
		if len(containers) > 0 {
			return containers[0].Image, nil
		}
	}
	if dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, service, metav1.GetOptions{}); err == nil {
		containers := dep.Spec.Template.Spec.Containers
		if len(containers) > 0 {
			return containers[0].Image, nil
		}
	}
	return "", fmt.Errorf("deployment for %s not found in %s", service, namespace)
}

// PatchCRStatus is a thin wrapper around PatchCRStatusFull that updates
// only the phase field.
func (c *Client) PatchCRStatus(ctx context.Context, name, namespace, phase string) error {
	return c.PatchCRStatusFull(ctx, name, namespace, phase, "", "", 0, "")
}

// PatchCRStatusFull patches all status fields of a SentinelIncident CR.
// Names that aren't real CRs ("", "auto-detected", "manual", "slack") are
// silently skipped — they come from non-CRD trigger paths.
func (c *Client) PatchCRStatusFull(ctx context.Context, name, namespace, phase, incidentID, rootCause string, confidence float64, prURL string) error {
	if name == "" || name == "auto-detected" || name == "manual" || name == "slack" {
		return nil
	}

	gvr := schema.GroupVersionResource{
		Group:    "sentinel.io",
		Version:  "v1alpha1",
		Resource: "sentinelincidents",
	}

	status := map[string]any{
		"phase":       phase,
		"lastUpdated": time.Now().UTC().Format(time.RFC3339),
	}
	if incidentID != "" {
		status["incidentId"] = incidentID
	}
	if rootCause != "" {
		status["rootCause"] = rootCause
	}
	if confidence > 0 {
		status["confidence"] = confidence
	}
	if prURL != "" {
		status["prUrl"] = prURL
	}

	patch := map[string]any{"status": status}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal patch: %w", err)
	}

	_, err = c.dynamic.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status",
	)
	return err
}

// ParseStackTrace extracts candidate source-file paths from a stack trace
// or log line. Filters out URLs, asset extensions (.png/.css/.json/...),
// and stdlib paths (/usr/lib, /site-packages, ...).
func ParseStackTrace(text string) []string {
	candidates := fileRePre.FindAllString(text, -1)

	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		c = strings.Trim(c, "'\"`")
		lower := strings.ToLower(c)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			continue
		}
		if hasAssetExt(lower) {
			continue
		}
		if isStdlibPath(c) {
			continue
		}
		if strings.HasPrefix(c, "/") && !strings.HasPrefix(c, "//") {
			c = c[1:]
		}
		if !strings.Contains(c, "/") && strings.Count(c, ".") < 2 {
			continue
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func hasAssetExt(s string) bool {
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
		".css", ".html", ".json", ".yaml", ".yml", ".toml", ".md", ".txt", ".lock"} {
		if strings.HasSuffix(s, ext) {
			return true
		}
	}
	return false
}

func isStdlibPath(s string) bool {
	return strings.Contains(s, "/site-packages/") ||
		strings.Contains(s, "/dist-packages/") ||
		strings.HasPrefix(s, "/usr/local/lib/") ||
		strings.HasPrefix(s, "/usr/lib/") ||
		strings.HasPrefix(s, "/lib/") ||
		strings.HasPrefix(s, "/opt/")
}

// SortedKeys returns the keys of m sorted lexicographically.
func SortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mergeAnnotations(dst map[string]string, sources ...map[string]string) {
	for _, src := range sources {
		for k, v := range src {
			if sentinelAnnotationKeys[k] {
				dst[k] = v
			}
		}
	}
}
