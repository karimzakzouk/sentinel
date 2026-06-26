// Package watcher implements the SentinelIncident CRD watcher.
// v3: uses a real Kubernetes Watch() stream — detection latency ~1s (was 10s polling in v1.x).
// Falls back to polling if the watch stream breaks.
package watcher

import (
        "context"
        "fmt"
        "log"
        "time"

        "github.com/google/uuid"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
        "k8s.io/apimachinery/pkg/runtime/schema"
        "k8s.io/apimachinery/pkg/watch"
        "k8s.io/client-go/dynamic"

        "github.com/karimzakzouk/sentinel/v3/internal/k8s"
        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

var sentinelIncidentGVR = schema.GroupVersionResource{
        Group:    "sentinel.io",
        Version:  "v1alpha1",
        Resource: "sentinelincidents",
}

// Handler is called for every new SentinelIncident CR. It receives the
// synthetic anomaly cluster (with real pod logs attached) and should run
// the reasoning pipeline.
type Handler func(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string)

// Watcher watches SentinelIncident CRs and calls the Handler for new ones.
type Watcher struct {
        k8s     *k8s.Client
        dynamic dynamic.Interface
        handler Handler
}

// New creates a Watcher.
func New(k8sClient *k8s.Client, handler Handler) (*Watcher, error) {
        return &Watcher{
                k8s:     k8sClient,
                dynamic: k8sClient.DynamicClient(),
                handler: handler,
        }, nil
}

// Run starts the watch loop. Blocks until ctx is cancelled.
// Uses a real Watch() stream; falls back to polling on stream errors.
func (w *Watcher) Run(ctx context.Context) {
        for {
                select {
                case <-ctx.Done():
                        return
                default:
                }

                err := w.watchStream(ctx)
                if err != nil {
                        log.Printf("[watcher] watch stream error: %v — falling back to poll for 30s", err)
                        w.pollFallback(ctx, 30*time.Second)
                }
        }
}

func (w *Watcher) watchStream(ctx context.Context) error {
        // List with resourceVersion="" to get the current state first, then watch.
        list, err := w.dynamic.Resource(sentinelIncidentGVR).List(ctx, metav1.ListOptions{})
        if err != nil {
                return fmt.Errorf("list CRs: %w", err)
        }

        resourceVersion := list.GetResourceVersion()

        for i := range list.Items {
                item := &list.Items[i]
                // On startup, skip CRs that already have a status.phase (already processed)
                status, _, _ := unstructured.NestedString(item.Object, "status", "phase")
                if status != "" {
                        continue
                }
                w.handleItem(ctx, item)
        }

        // Now watch for new events
        watcher, err := w.dynamic.Resource(sentinelIncidentGVR).Watch(ctx, metav1.ListOptions{
                ResourceVersion: resourceVersion,
        })
        if err != nil {
                return fmt.Errorf("watch: %w", err)
        }
        defer watcher.Stop()

        for {
                select {
                case <-ctx.Done():
                        return nil
                case event, ok := <-watcher.ResultChan():
                        if !ok {
                                return fmt.Errorf("watch channel closed")
                        }
                        if event.Type != watch.Added && event.Type != watch.Modified {
                                continue
                        }
                        item, ok := event.Object.(*unstructured.Unstructured)
                        if !ok {
                                continue
                        }
                        // Only handle CRs without status.phase (new)
                        status, _, _ := unstructured.NestedString(item.Object, "status", "phase")
                        if status != "" {
                                continue
                        }
                        w.handleItem(ctx, item)
                }
        }
}

func (w *Watcher) handleItem(ctx context.Context, item *unstructured.Unstructured) {
        name := item.GetName()
        namespace := item.GetNamespace()

        spec, _, _ := unstructured.NestedMap(item.Object, "spec")
        service, _ := spec["service"].(string)
        if service == "" {
                service = "unknown"
        }
        svcNs, _ := spec["namespace"].(string)
        if svcNs == "" {
                svcNs = namespace
        }
        reason, _ := spec["reason"].(string)
        if reason == "" {
                reason = "triggered by CRD"
        }
        repoFromSpec, _ := spec["repo"].(string)

        log.Printf("[watcher] new SentinelIncident %s/%s (service=%s, reason=%s)", namespace, name, service, reason)

        // Patch CR status to "investigating" so we don't re-trigger.
        // This is the only synchronous K8s API call in the watch loop — fast.
        if err := w.k8s.PatchCRStatus(ctx, name, namespace, "investigating"); err != nil {
                log.Printf("[watcher] could not patch CR status: %v", err)
        }

        // v3.13.0: Move the slow lookups (annotations + pod logs) into the handler
        // goroutine so the watch loop isn't blocked for ~10s per CR. Previously,
        // LookupWorkloadAnnotations (5 sequential K8s API calls) + FetchCrashingPodLogs
        // (3 retries × 2s = up to 6s) ran synchronously before spawning the handler.
        // Under a burst of CRs (cascade), the watch fell behind and missed events.
        // Also pass ctx (not context.Background()) so in-flight handlers stop on SIGTERM.
        if w.handler != nil {
                go func() {
                        // Discover sentinel.io/* annotations from the affected workload
                        annotations, _ := w.k8s.LookupWorkloadAnnotations(ctx, service, svcNs)
                        if repoFromSpec != "" {
                                if annotations == nil {
                                        annotations = map[string]string{}
                                }
                                annotations["sentinel.io/repo"] = repoFromSpec
                        }
                        if len(annotations) > 0 {
                                log.Printf("[watcher] discovered annotations: %v", annotations)
                        } else {
                                log.Printf("[watcher] no annotations found on workload %s/%s — set sentinel.io/repo + sentinel.io/branch annotations on the Deployment", svcNs, service)
                        }

                        // Fetch REAL pod logs from the crashing pods
                        podLogs, _ := w.k8s.FetchCrashingPodLogs(ctx, service, svcNs)
                        if podLogs != "" {
                                log.Printf("[watcher] fetched %d chars of pod logs for %s", len(podLogs), service)
                        }

                        // Build the synthetic anomaly cluster
                        explanation := fmt.Sprintf("SentinelIncident CR: %s", reason)
                        if podLogs != "" {
                                explanation += fmt.Sprintf("\n\n## Pod Logs (last 4KB)\n%s", podLogs)
                        }

                        cluster := schemas.AnomalyCluster{
                                ClusterID: uuid.New(),
                                Service:   service,
                                Namespace: svcNs,
                                Anomalies: []schemas.Anomaly{
                                        {
                                                AnomalyID:   uuid.New(),
                                                SignalID:    uuid.New(),
                                                Service:     service,
                                                Namespace:   svcNs,
                                                Detector:    "crd_watcher",
                                                Score:       0.8,
                                                Dimensions:  map[string]float64{"manual": 1.0},
                                                Explanation: explanation,
                                                Annotations: annotations,
                                        },
                                },
                                Summary:     fmt.Sprintf("SentinelIncident: %s", reason),
                                Severity:    schemas.SeverityWarning,
                                Annotations: annotations,
                        }

                        w.handler(ctx, cluster, name, namespace)
                }()
        }
}

func (w *Watcher) pollFallback(ctx context.Context, duration time.Duration) {
        deadline := time.Now().Add(duration)
        for time.Now().Before(deadline) {
                select {
                case <-ctx.Done():
                        return
                default:
                }

                list, err := w.dynamic.Resource(sentinelIncidentGVR).List(ctx, metav1.ListOptions{})
                if err != nil {
                        time.Sleep(15 * time.Second)
                        continue
                }

                for i := range list.Items {
                        item := &list.Items[i]
                        status, _, _ := unstructured.NestedString(item.Object, "status", "phase")
                        if status != "" {
                                continue
                        }
                        w.handleItem(ctx, item)
                }

                time.Sleep(10 * time.Second)
        }
}
