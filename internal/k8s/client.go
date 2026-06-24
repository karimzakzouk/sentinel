// Package k8s wraps the Kubernetes client with the helper functions Sentinel needs.
// v3: single binary, single client, no Python client-go shim.
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
        "k8s.io/client-go/dynamic"
        "k8s.io/client-go/kubernetes"
        "k8s.io/client-go/rest"
        "k8s.io/client-go/tools/clientcmd"
)

// Sentinel annotation keys we recognize on workloads.
var sentinelAnnotationKeys = map[string]bool{
        "sentinel.io/repo":         true,
        "sentinel.io/branch":       true,
        "sentinel.io/git-commit":   true,
        "sentinel.io/runbook":      true,
        "sentinel.io/team":         true,
        "sentinel.io/ignore":       true,
        "sentinel.io/owner":        true,
        "sentinel.io/criticality":  true,
}

// Client wraps the K8s clientset with the helpers Sentinel needs.
type Client struct {
        clientset *kubernetes.Clientset
        dynamic   dynamic.Interface
        config    *rest.Config
}

// New creates a Client using in-cluster config, with fallback to the local
// kubeconfig for dev/testing.
//
// v3.8.4: Previously only tried rest.InClusterConfig() and hard-crashed
// (log.Fatalf in main.go) when not running inside a pod. That made local
// testing impossible — the server wouldn't even start without
// KUBERNETES_SERVICE_HOST being set. Now tries in-cluster first, then
// falls back to ~/.kube/config (or $KUBECONFIG), matching the behavior
// of every other K8s tool (kubectl, helm, client-go examples).
//
// If neither works, returns an error so main.go can decide whether to
// fatal (production) or log-and-continue (local dev with --no-k8s flag,
// which is a TODO).
func New() (*Client, error) {
        config, err := rest.InClusterConfig()
        if err != nil {
                // Fall back to local kubeconfig for dev/testing.
                // v3.8.4: Use client-go's clientcmd loading rules which respect
                // KUBECONFIG env var and default to ~/.kube/config.
                loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
                configOverrides := &clientcmd.ConfigOverrides{}
                clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
                cfg, cfgErr := clientConfig.ClientConfig()
                if cfgErr != nil {
                        return nil, fmt.Errorf("in-cluster config: %w; kubeconfig fallback: %v", err, cfgErr)
                }
                config = cfg
        }
        clientset, err := kubernetes.NewForConfig(config)
        if err != nil {
                return nil, fmt.Errorf("create clientset: %w", err)
        }
        dyn, err := dynamic.NewForConfig(config)
        if err != nil {
                return nil, fmt.Errorf("create dynamic client: %w", err)
        }
        return &Client{clientset: clientset, dynamic: dyn, config: config}, nil
}

// Clientset returns the underlying kubernetes clientset (for sandbox + executors).
func (c *Client) Clientset() *kubernetes.Clientset {
        return c.clientset
}

// DynamicClient returns the dynamic interface (for CRD operations).
func (c *Client) DynamicClient() dynamic.Interface {
        return c.dynamic
}

// LookupWorkloadAnnotations finds sentinel.io/* annotations on the workload
// matching `service` in `namespace`. Tries Deployment by name → by label →
// StatefulSet → Pod.
func (c *Client) LookupWorkloadAnnotations(ctx context.Context, service, namespace string) (map[string]string, error) {
        annotations := map[string]string{}

        // 1. Try Deployment by exact name
        if dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, service, metav1.GetOptions{}); err == nil {
                mergeAnnotations(annotations, dep.Annotations, dep.Spec.Template.Annotations)
                if len(annotations) > 0 {
                        return annotations, nil
                }
        }

        // 2. Try Deployment by label app.kubernetes.io/name=service
        if deps, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
                LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
        }); err == nil && len(deps.Items) > 0 {
                dep := &deps.Items[0]
                mergeAnnotations(annotations, dep.Annotations, dep.Spec.Template.Annotations)
                if len(annotations) > 0 {
                        return annotations, nil
                }
        }

        // 3. Try Deployment by label app=service (older convention)
        if deps, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
                LabelSelector: fmt.Sprintf("app=%s", service),
        }); err == nil && len(deps.Items) > 0 {
                dep := &deps.Items[0]
                mergeAnnotations(annotations, dep.Annotations, dep.Spec.Template.Annotations)
                if len(annotations) > 0 {
                        return annotations, nil
                }
        }

        // 4. Try StatefulSet by exact name
        if sts, err := c.clientset.AppsV1().StatefulSets(namespace).Get(ctx, service, metav1.GetOptions{}); err == nil {
                mergeAnnotations(annotations, sts.Annotations, sts.Spec.Template.Annotations)
                if len(annotations) > 0 {
                        return annotations, nil
                }
        }

        // 5. Try Pods by label
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

// FetchCrashingPodLogs returns the last 4KB of logs from the most-crashing pod
// matching `service` in `namespace`. Retries up to 3 times with delays because
// K8s sometimes returns "unable to retrieve container logs for containerd://..."
// when the container just restarted and logs aren't available yet.
//
// Tries previous container logs first (the crash output), then current logs.
// Returns the first non-empty, non-error log content.
func (c *Client) FetchCrashingPodLogs(ctx context.Context, service, namespace string) (string, error) {
        // Find pods matching the service label
        pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
                LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
        })
        if err != nil || len(pods.Items) == 0 {
                // Try app=service label
                pods, err = c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
                        LabelSelector: fmt.Sprintf("app=%s", service),
                })
                if err != nil || len(pods.Items) == 0 {
                        return "", nil
                }
        }

        // Pick the worst pod: prefer CrashLoopBackOff, else most restarts
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

        // Retry log fetching up to 3 times with 2s delays.
        // K8s sometimes returns "unable to retrieve container logs for containerd://..."
        // immediately after a restart; waiting 2s usually fixes it.
        limitBytes := int64(8192) // 8KB — enough for a full stack trace
        for attempt := 0; attempt < 3; attempt++ {
                // Try previous container logs first (the crash output)
                opts := &corev1.PodLogOptions{
                        Previous:   true,
                        LimitBytes: &limitBytes,
                }
                if logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(worstPod.Name, opts).Do(ctx).Raw(); err == nil {
                        s := string(logs)
                        if isValidLogContent(s) {
                                return s, nil
                        }
                }

                // Fall back to current logs
                opts.Previous = false
                if logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(worstPod.Name, opts).Do(ctx).Raw(); err == nil {
                        s := string(logs)
                        if isValidLogContent(s) {
                                return s, nil
                        }
                }

                // Wait before retrying
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

// isValidLogContent returns false if the log content is empty or is just the
// K8s error message "unable to retrieve container logs for containerd://...".
// This happens when the container just restarted and logs aren't available yet.
func isValidLogContent(s string) bool {
        if strings.TrimSpace(s) == "" {
                return false
        }
        // K8s returns this error string when containerd logs aren't ready yet
        if strings.Contains(s, "unable to retrieve container logs for containerd://") {
                // But it's still valid if there's other content after the error
                // (some runtimes prefix the error then include the actual logs)
                lines := strings.Split(s, "\n")
                realContent := 0
                for _, line := range lines {
                        line = strings.TrimSpace(line)
                        if line == "" || strings.Contains(line, "unable to retrieve container logs") {
                                continue
                        }
                        realContent++
                }
                return realContent > 0
        }
        return true
}

// GetProdImage returns the current production image for a service by looking
// up its Deployment's first container image.
func (c *Client) GetProdImage(ctx context.Context, service, namespace string) (string, error) {
        // Try by label
        if deps, err := c.clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
                LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
        }); err == nil && len(deps.Items) > 0 {
                containers := deps.Items[0].Spec.Template.Spec.Containers
                if len(containers) > 0 {
                        return containers[0].Image, nil
                }
        }

        // Try by exact name
        if dep, err := c.clientset.AppsV1().Deployments(namespace).Get(ctx, service, metav1.GetOptions{}); err == nil {
                containers := dep.Spec.Template.Spec.Containers
                if len(containers) > 0 {
                        return containers[0].Image, nil
                }
        }

        return "", fmt.Errorf("deployment for %s not found in %s", service, namespace)
}

// PatchCRStatus patches the status.phase of a SentinelIncident CR.
func (c *Client) PatchCRStatus(ctx context.Context, name, namespace, phase string) error {
        gvr := schema.GroupVersionResource{
                Group:    "sentinel.io",
                Version:  "v1alpha1",
                Resource: "sentinelincidents",
        }

        patch := map[string]any{
                "status": map[string]any{
                        "phase":       phase,
                        "lastUpdated": time.Now().UTC().Format(time.RFC3339),
                },
        }
        patchBytes, err := json.Marshal(patch)
        if err != nil {
                return fmt.Errorf("marshal patch: %w", err)
        }

        _, err = c.dynamic.Resource(gvr).Namespace(namespace).Patch(
                ctx, name, typesMergePatch, patchBytes, metav1.PatchOptions{}, "status",
        )
        return err
}

// ParseStackTrace extracts source file paths from a stack trace or log line.
// Filters out URLs, image extensions, and stdlib paths.
func ParseStackTrace(text string) []string {
        fileRe := regexp.MustCompile(`[a-zA-Z0-9_./-]+\.[a-zA-Z]{1,5}`)
        candidates := fileRe.FindAllString(text, -1)

        seen := map[string]bool{}
        var out []string
        for _, c := range candidates {
                c = strings.Trim(c, "'\"`")
                // Skip URLs
                lower := strings.ToLower(c)
                if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
                        continue
                }
                // Skip asset extensions
                skip := false
                for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
                        ".css", ".html", ".json", ".yaml", ".yml", ".toml", ".md", ".txt", ".lock"} {
                        if strings.HasSuffix(c, ext) {
                                skip = true
                                break
                        }
                }
                if skip {
                        continue
                }
                // Skip stdlib paths
                if strings.Contains(c, "/site-packages/") || strings.Contains(c, "/dist-packages/") ||
                        strings.HasPrefix(c, "/usr/local/lib/") || strings.HasPrefix(c, "/usr/lib/") ||
                        strings.HasPrefix(c, "/lib/") || strings.HasPrefix(c, "/opt/") {
                        continue
                }
                // Strip leading slash
                if strings.HasPrefix(c, "/") && !strings.HasPrefix(c, "//") {
                        c = c[1:]
                }
                // Require either a slash or 2+ dots (Java class file)
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

// SortedKeys returns the keys of a map, sorted (for deterministic output).
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
