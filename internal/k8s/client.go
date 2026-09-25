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

	"github.com/karimzakzouk/sentinel/internal/schemas"
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

// Client wraps the Kubernetes clientset + dynamic client. The clientset
// field is the kubernetes.Interface so tests can inject the fake
// clientset (same pattern as the detection package).
type Client struct {
	clientset kubernetes.Interface
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
func (c *Client) Clientset() kubernetes.Interface { return c.clientset }

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

// FetchCrashingPodLogs returns recent logs from the worst-crashing pod
// matching service in namespace. "Worst" means CrashLoopBackOff first,
// otherwise the pod with the most restarts. Tries previous-container logs
// (the crash output) first, then current logs. The fetch is TAIL-based
// (last ~300 lines, capped to the final 64KB): crash signatures — Rust
// panics, Node "FATAL ERROR: Ineffective mark-compacts", Go panics — sit
// at the END of the previous container's output, so a head-limited
// fetch (LimitBytes from the start) misses them on chatty pods (seen in
// the marketly eval run #10: an 8KB-from-the-start window swallowed
// Node's heap-exhaustion line and the investigation misdiagnosed).
// Retries up to 3 times with 2-second delays because K8s sometimes
// returns "unable to retrieve container logs for containerd://..."
// immediately after a restart.
//
// Returns ("", nil) if no matching pod is found or no valid log content
// could be retrieved.
//
// v1.7.13: the crash output lives in the PREVIOUS container instance's
// logs; the current instance usually shows only a fresh startup line. The
// old version fell back to current-container logs inside the first retry
// attempt, so a 27-byte "server starting" line shadowed the entire crash
// stack (run #27 user-api: "JavaScript heap out of memory" never reached
// the LLM, and it hallucinated a DB-connection fix). Now:
//   - previous-container logs are retried across the full window (5x3s);
//   - current-container logs are collected separately, never as a
//     substitute, and labeled for what they are;
//   - up to 3 pods are consulted, so a single pod's rotated-away logs
//     don't zero out the evidence.
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

	// Rank pods: CrashLoopBackOff first, then by restart count.
	type podCandidate struct {
		pod      *corev1.Pod
		restarts int
		crashing bool
	}
	var candidates []podCandidate
	for i := range pods.Items {
		pod := &pods.Items[i]
		cand := podCandidate{pod: pod}
		for _, cs := range pod.Status.ContainerStatuses {
			cand.restarts += int(cs.RestartCount)
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				cand.crashing = true
			}
		}
		if cand.crashing || cand.restarts > 0 {
			candidates = append(candidates, cand)
		}
	}
	// CrashLoopBackOff pods first, then highest restarts; stable order.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].crashing != candidates[j].crashing {
			return candidates[i].crashing
		}
		return candidates[i].restarts > candidates[j].restarts
	})
	if len(candidates) > 3 {
		candidates = candidates[:3]
	}
	if len(candidates) == 0 {
		return "", nil
	}

	limitBytes := int64(64 * 1024)
	tailLines := int64(300)

	// Phase 1: try previous-container logs on each candidate with the
	// full retry window — the crash stack is HERE when it is anywhere.
	for _, cand := range candidates {
		for range 5 {
			opts := &corev1.PodLogOptions{Previous: true, TailLines: &tailLines, LimitBytes: &limitBytes}
			if logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(cand.pod.Name, opts).Do(ctx).Raw(); err == nil && isValidLogContent(string(logs)) {
				out := fmt.Sprintf("## Crashed container logs (previous instance of pod %s — THIS is the actual crash output)\n%s",
					cand.pod.Name, keepLogTail(string(logs), int(limitBytes)))
				// Also grab the current instance's logs for context
				// (post-restart behavior) when available.
				opts.Previous = false
				if cur, err := c.clientset.CoreV1().Pods(namespace).GetLogs(cand.pod.Name, opts).Do(ctx).Raw(); err == nil && isValidLogContent(string(cur)) {
					out += fmt.Sprintf("\n\n## Current container logs (pod %s after restart — startup state, NOT the crash)\n%s",
						cand.pod.Name, keepLogTail(string(cur), 16*1024))
				}
				return out, nil
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(3 * time.Second):
			}
		}
	}

	// Phase 2: no previous logs anywhere (rotated away / GC'd). Return
	// the current logs of the worst candidate, honestly labeled.
	opts := &corev1.PodLogOptions{TailLines: &tailLines, LimitBytes: &limitBytes}
	if logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(candidates[0].pod.Name, opts).Do(ctx).Raw(); err == nil && isValidLogContent(string(logs)) {
		return fmt.Sprintf("## Container logs (pod %s, CURRENT instance only — the crashed instance's logs are unavailable; the crash reason must be inferred from restart behavior and the code)\n%s",
			candidates[0].pod.Name, keepLogTail(string(logs), int(limitBytes))), nil
	}
	return "", nil
}

// keepLogTail returns the final limit bytes of s. The K8s API applies
// LimitBytes from the START of the (tailed) line set, so on pods whose
// last 300 lines exceed the budget the signature lines at the very end
// would be cut; trim in-process instead so the END always survives.
func keepLogTail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[len(s)-limit:]
}

// FetchPodRuntimeContext reads configuration ground truth from the first
// matching pod's primary container (v1.7.4): env var NAMES (never values),
// configmap/secret references, resource requests/limits, image, and the
// entrypoint. This exists so the LLM stages never have to guess what the
// environment provides — the run-#11 user-api incident produced a harmful
// "fix" (renaming USER_DATABASE_URL to a canonical DATABASE_URL the
// deployment never sets) precisely because the pod's env was invisible to
// the investigation.
//
// Best-effort: returns (nil, nil) when no pod matches. Selector logic
// mirrors FetchCrashingPodLogs (app.kubernetes.io/name, then app=).
func (c *Client) FetchPodRuntimeContext(ctx context.Context, service, namespace string) (*schemas.PodRuntimeContext, error) {
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
	})
	if err != nil || len(pods.Items) == 0 {
		pods, err = c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", service),
		})
		if err != nil || len(pods.Items) == 0 {
			return nil, nil
		}
	}

	pod := &pods.Items[0]
	rc := &schemas.PodRuntimeContext{
		PodName: pod.Name,
	}

	// Prefer the first container that isn't a sandbox/sidecar pattern;
	// fall back to containers[0] (consistent with log fetching).
	var ctr *corev1.Container
	for i := range pod.Spec.Containers {
		ci := &pod.Spec.Containers[i]
		n := strings.ToLower(ci.Name)
		if strings.Contains(n, "istio-proxy") || strings.Contains(n, "vault-agent") || strings.Contains(n, "sentinel-sandbox") {
			continue
		}
		ctr = ci
		break
	}
	if ctr == nil && len(pod.Spec.Containers) > 0 {
		ctr = &pod.Spec.Containers[0]
	}
	if ctr == nil {
		return rc, nil
	}

	rc.ContainerName = ctr.Name
	rc.Image = ctr.Image
	rc.Command = ctr.Command

	for _, env := range ctr.Env {
		rc.EnvVarNames = append(rc.EnvVarNames, env.Name)
		if env.ValueFrom == nil {
			continue
		}
		if ref := env.ValueFrom.ConfigMapKeyRef; ref != nil && ref.Name != "" {
			rc.ConfigMapRefs = appendUnique(rc.ConfigMapRefs, ref.Name)
		}
		if ref := env.ValueFrom.SecretKeyRef; ref != nil && ref.Name != "" {
			rc.SecretRefs = appendUnique(rc.SecretRefs, ref.Name)
		}
	}
	for _, vol := range pod.Spec.Volumes {
		if vol.ConfigMap != nil && vol.ConfigMap.Name != "" {
			rc.ConfigMapRefs = appendUnique(rc.ConfigMapRefs, vol.ConfigMap.Name)
		}
		if vol.Secret != nil && vol.Secret.SecretName != "" {
			rc.SecretRefs = appendUnique(rc.SecretRefs, vol.Secret.SecretName)
		}
	}

	if rq := ctr.Resources.Requests; rq != nil {
		rc.CPURequest = rq.Cpu().String()
		rc.MemoryRequest = rq.Memory().String()
	}
	if lm := ctr.Resources.Limits; lm != nil {
		rc.CPULimit = lm.Cpu().String()
		rc.MemoryLimit = lm.Memory().String()
	}

	sort.Strings(rc.EnvVarNames)
	sort.Strings(rc.ConfigMapRefs)
	sort.Strings(rc.SecretRefs)
	return rc, nil
}

// appendUnique appends s to slice if not already present (dedup refs).
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
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
