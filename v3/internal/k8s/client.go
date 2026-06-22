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
)

// Sentinel annotation keys we recognize on workloads.
var sentinelAnnotationKeys = map[string]bool{
        "sentinel.io/repo":         true,
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

// New creates a Client using in-cluster config.
func New() (*Client, error) {
        config, err := rest.InClusterConfig()
        if err != nil {
                return nil, fmt.Errorf("in-cluster config: %w", err)
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
// matching `service` in `namespace`. Tries previous container logs first
// (the crash output), falls back to current logs.
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

        // Try previous container logs first (the crash output)
        limitBytes := int64(4096)
        opts := &corev1.PodLogOptions{
                Previous:   true,
                LimitBytes: &limitBytes,
        }
        if logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(worstPod.Name, opts).Do(ctx).Raw(); err == nil {
                return string(logs), nil
        }

        // Fall back to current logs
        opts.Previous = false
        if logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(worstPod.Name, opts).Do(ctx).Raw(); err == nil {
                return string(logs), nil
        }

        return "", nil
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
