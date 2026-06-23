package detection

import (
        "context"
        "fmt"
        "log"
        "strings"
        "sync"
        "time"

        corev1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/client-go/kubernetes"
)

// SignalType is what kind of detector produced this signal.
type SignalType string

const (
        SignalLogError  SignalType = "log_error"
        SignalPodStatus SignalType = "pod_status"
        SignalK8sEvent  SignalType = "k8s_event"
)

// Signal is what detectors send to the clusterer.
type Signal struct {
        Type      SignalType
        Service   string
        Namespace string
        Reason    string
        PodLogs   string
        Repo      string
        Timestamp time.Time
}

// Clusterer groups signals by service within a time window and emits
// ONE anomaly cluster per service per window.
type Clusterer struct {
        handler    IncidentHandler
        tracker    *ActiveTracker
        preprocess *LogPreprocessor
        clientset  *kubernetes.Clientset

        mu      sync.Mutex
        pending map[string]*pendingCluster
        window  time.Duration
}

type pendingCluster struct {
        signals   []Signal
        firstSeen time.Time
        timer     *time.Timer
}

// NewClusterer creates a Clusterer with K8s client for full log fetching.
func NewClusterer(handler IncidentHandler, tracker *ActiveTracker, clientset *kubernetes.Clientset) *Clusterer {
        return &Clusterer{
                handler:    handler,
                tracker:     tracker,
                preprocess:  NewLogPreprocessor(),
                clientset:   clientset,
                pending:     make(map[string]*pendingCluster),
                window:      5 * time.Second,
        }
}

// Send sends a signal to the clusterer.
func (c *Clusterer) Send(ctx context.Context, sig Signal) {
        key := sig.Service + "/" + sig.Namespace

        c.mu.Lock()
        defer c.mu.Unlock()

        if pending, exists := c.pending[key]; exists {
                pending.signals = append(pending.signals, sig)
                if len(sig.PodLogs) > len(pending.signals[0].PodLogs) {
                        pending.signals[0].PodLogs = sig.PodLogs
                }
                if len(sig.Reason) > len(pending.signals[0].Reason) {
                        pending.signals[0].Reason = sig.Reason
                }
                return
        }

        pending := &pendingCluster{
                signals:   []Signal{sig},
                firstSeen: time.Now(),
        }
        c.pending[key] = pending

        pending.timer = time.AfterFunc(c.window, func() {
                c.emit(key)
        })
}

// emit fires the cluster after the grouping window closes.
func (c *Clusterer) emit(key string) {
        c.mu.Lock()
        pending, exists := c.pending[key]
        if !exists {
                c.mu.Unlock()
                return
        }
        delete(c.pending, key)
        c.mu.Unlock()

        if c.tracker.IsActive(pending.signals[0].Service, pending.signals[0].Namespace) {
                log.Printf("[clusterer] skipping %s — active incident already in progress", key)
                return
        }

        sig := pending.signals[0]

        // v3.2.3: ALWAYS fetch full pod logs via K8s API — don't rely on the
        // log scanner's truncated capture. The scanner detects the error (trigger),
        // but the LLM needs the FULL traceback (including the error type at the bottom).
        fullLogs := c.fetchFullPodLogs(sig.Service, sig.Namespace)

        // Use the full K8s API logs if available (they're always complete),
        // otherwise fall back to whatever the scanner captured.
        logsToUse := fullLogs
        if logsToUse == "" {
                logsToUse = sig.PodLogs
        }

        // Preprocess the logs
        processedLogs := c.preprocess.Process(logsToUse)

        var signalTypes []string
        for _, s := range pending.signals {
                signalTypes = append(signalTypes, string(s.Type))
        }

        c.tracker.MarkActive(sig.Service, sig.Namespace)

        log.Printf("[clusterer] emitting cluster for %s (signals: %s, logs: %d chars)",
                key, strings.Join(signalTypes, "+"), len(processedLogs))

        if c.handler != nil {
                go c.handler(sig.Service, sig.Namespace, sig.Reason, processedLogs)
        }
}

// fetchFullPodLogs calls the K8s API to get the complete 8KB of pod logs.
// This gets the FULL traceback including the error type at the bottom,
// which the live log scanner's truncated capture often misses.
func (c *Clusterer) fetchFullPodLogs(service, namespace string) string {
        if c.clientset == nil {
                return ""
        }

        ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
        defer cancel()

        // Find pods matching the service
        pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
                LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
        })
        if err != nil || len(pods.Items) == 0 {
                pods, err = c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
                        LabelSelector: fmt.Sprintf("app=%s", service),
                })
                if err != nil || len(pods.Items) == 0 {
                        return ""
                }
        }

        // Pick the worst pod
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
                // Just use the first pod
                worstPod = &pods.Items[0]
        }

        containerName := ""
        if len(worstPod.Spec.Containers) > 0 {
                containerName = worstPod.Spec.Containers[0].Name
        }

        limitBytes := int64(8192)

        // Try previous logs first (crash output)
        opts := &corev1.PodLogOptions{
                Previous:   true,
                LimitBytes: &limitBytes,
                Container:  containerName,
        }
        if logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(worstPod.Name, opts).Do(ctx).Raw(); err == nil {
                s := string(logs)
                if isValidLogContent(s) {
                        return s
                }
        }

        // Fall back to current logs
        opts.Previous = false
        if logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(worstPod.Name, opts).Do(ctx).Raw(); err == nil {
                s := string(logs)
                if isValidLogContent(s) {
                        return s
                }
        }

        return ""
}

func isValidLogContent(s string) bool {
        if strings.TrimSpace(s) == "" {
                return false
        }
        if strings.Contains(s, "unable to retrieve container logs for containerd://") {
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
