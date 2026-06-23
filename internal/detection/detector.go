// Package detection implements the autonomous detection layer for Sentinel v3.1.
//
// Two live watchers running continuously:
//   1. Log Stream Manager — streams logs from every pod with sentinel.io/repo
//      annotation, scans for error patterns in real-time, auto-triggers incidents
//   2. Pod Status Watcher — watches pod status changes, auto-triggers on
//      CrashLoopBackOff, OOMKilled, ImagePullBackOff
//
// Both are live (streaming, not polling). Both feed into the existing pipeline.
package detection

import (
        "bufio"
        "context"
        "fmt"
        "log"
        "regexp"
        "strings"
        "sync"
        "time"

        corev1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/client-go/kubernetes"
)

// ErrorPattern matches error lines in pod logs across languages.
var errorPatterns = []*regexp.Regexp{
        // Python
        regexp.MustCompile(`Traceback \(most recent call last\)`),
        regexp.MustCompile(`^\w+Error:`),
        regexp.MustCompile(`^\w+Exception:`),
        // Go
        regexp.MustCompile(`panic:`),
        regexp.MustCompile(`fatal error:`),
        regexp.MustCompile(`goroutine \d+ \[running\]:`),
        // Node.js / JavaScript
        regexp.MustCompile(`TypeError:`),
        regexp.MustCompile(`ReferenceError:`),
        regexp.MustCompile(`UnhandledPromiseRejection`),
        regexp.MustCompile(`SyntaxError:`),
        // Java
        regexp.MustCompile(`java\.lang\.\w+Exception`),
        regexp.MustCompile(`NullPointerException`),
        regexp.MustCompile(`StackOverflowError`),
        // Rust
        regexp.MustCompile(`thread '.*' panicked`),
        regexp.MustCompile(`panic!`),
        // Ruby
        regexp.MustCompile(`\.rb:\d+:.*Error`),
        // Generic
        regexp.MustCompile(`FATAL`),
        regexp.MustCompile(`SEGFAULT`),
        regexp.MustCompile(`OOMKilled`),
        regexp.MustCompile(`^\[ERROR\]`),
        regexp.MustCompile(`level=error`),
        regexp.MustCompile(`level=fatal`),
}

// ErrorFingerprint uniquely identifies an error for deduplication.
type ErrorFingerprint struct {
        Service   string
        Namespace string
        ErrorType string
}

// IncidentHandler is called when an error is detected.
type IncidentHandler func(service, namespace, reason, podLogs string)

// Detector is the autonomous detection layer.
type Detector struct {
        clientset *kubernetes.Clientset
        handler   IncidentHandler
        alertURL  string

        mu         sync.Mutex
        recentSeen map[ErrorFingerprint]time.Time

        streams   map[string]context.CancelFunc
        streamsMu sync.Mutex
}

// New creates a Detector.
func New(clientset *kubernetes.Clientset, handler IncidentHandler, alertURL string) *Detector {
        return &Detector{
                clientset:  clientset,
                handler:    handler,
                alertURL:   alertURL,
                recentSeen: make(map[ErrorFingerprint]time.Time),
                streams:    make(map[string]context.CancelFunc),
        }
}

// Run starts the detection layer. Blocks until ctx is cancelled.
func (d *Detector) Run(ctx context.Context) {
        log.Printf("[detection] starting autonomous detection layer")

        go d.cleanupLoop(ctx)
        d.watchPods(ctx)
}

func (d *Detector) watchPods(ctx context.Context) {
        namespaces, err := d.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
        if err != nil {
                log.Printf("[detection] failed to list namespaces: %v", err)
                return
        }

        for _, ns := range namespaces.Items {
                go d.watchPodsInNamespace(ctx, ns.Name)
        }

        go d.watchNamespaces(ctx)
        <-ctx.Done()
}

func (d *Detector) watchNamespaces(ctx context.Context) {
        watcher, err := d.clientset.CoreV1().Namespaces().Watch(ctx, metav1.ListOptions{})
        if err != nil {
                return
        }
        defer watcher.Stop()

        for {
                select {
                case <-ctx.Done():
                        return
                case event, ok := <-watcher.ResultChan():
                        if !ok {
                                return
                        }
                        ns, ok := event.Object.(*corev1.Namespace)
                        if !ok || ns.Status.Phase != corev1.NamespaceActive {
                                continue
                        }
                        go d.watchPodsInNamespace(ctx, ns.Name)
                }
        }
}

func (d *Detector) watchPodsInNamespace(ctx context.Context, namespace string) {
        if namespace == "kube-system" || namespace == "kube-public" || namespace == "kube-node-lease" {
                return
        }

        for {
                select {
                case <-ctx.Done():
                        return
                default:
                }

                pods, err := d.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
                if err != nil {
                        time.Sleep(30 * time.Second)
                        continue
                }

                for i := range pods.Items {
                        d.handlePod(ctx, &pods.Items[i])
                }

                watcher, err := d.clientset.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
                        ResourceVersion: pods.ResourceVersion,
                })
                if err != nil {
                        time.Sleep(15 * time.Second)
                        continue
                }

                for {
                        select {
                        case <-ctx.Done():
                                watcher.Stop()
                                return
                        case event, ok := <-watcher.ResultChan():
                                if !ok {
                                        watcher.Stop()
                                        time.Sleep(5 * time.Second)
                                        goto restart
                                }
                                pod, ok := event.Object.(*corev1.Pod)
                                if !ok {
                                        continue
                                }
                                d.handlePod(ctx, pod)
                        }
                }

        restart:
        }
}

func (d *Detector) handlePod(ctx context.Context, pod *corev1.Pod) {
        annotations := pod.Annotations
        if annotations == nil {
                return
        }
        repo, hasRepo := annotations["sentinel.io/repo"]
        if !hasRepo {
                return
        }

        d.checkPodStatus(ctx, pod, repo)

        if pod.Status.Phase == corev1.PodRunning {
                d.startLogStream(ctx, pod, repo)
        }
}

func (d *Detector) checkPodStatus(ctx context.Context, pod *corev1.Pod, repo string) {
        for _, cs := range pod.Status.ContainerStatuses {
                if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
                        if d.shouldTrigger(pod.Namespace, d.getServiceFromPod(pod), "CrashLoopBackOff") {
                                log.Printf("[detection] CrashLoopBackOff: %s/%s", pod.Namespace, pod.Name)
                                logs := d.fetchPodLogs(ctx, pod)
                                reason := fmt.Sprintf("CrashLoopBackOff: %s", cs.State.Waiting.Message)
                                d.triggerIncident(pod, repo, reason, logs)
                        }
                        return
                }
                if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
                        if d.shouldTrigger(pod.Namespace, d.getServiceFromPod(pod), "OOMKilled") {
                                log.Printf("[detection] OOMKilled: %s/%s", pod.Namespace, pod.Name)
                                logs := d.fetchPodLogs(ctx, pod)
                                d.triggerIncident(pod, repo, "OOMKilled — container exceeded memory limit", logs)
                        }
                        return
                }
                if cs.State.Waiting != nil && cs.State.Waiting.Reason == "ImagePullBackOff" {
                        if d.shouldTrigger(pod.Namespace, d.getServiceFromPod(pod), "ImagePullBackOff") {
                                log.Printf("[detection] ImagePullBackOff: %s/%s", pod.Namespace, pod.Name)
                                reason := fmt.Sprintf("ImagePullBackOff: %s", cs.State.Waiting.Message)
                                d.triggerIncident(pod, repo, reason, "")
                        }
                        return
                }
        }
}

func (d *Detector) startLogStream(ctx context.Context, pod *corev1.Pod, repo string) {
        podUID := string(pod.UID)

        d.streamsMu.Lock()
        if _, exists := d.streams[podUID]; exists {
                d.streamsMu.Unlock()
                return
        }
        streamCtx, cancel := context.WithCancel(ctx)
        d.streams[podUID] = cancel
        d.streamsMu.Unlock()

        go func() {
                defer func() {
                        d.streamsMu.Lock()
                        delete(d.streams, podUID)
                        d.streamsMu.Unlock()
                }()
                log.Printf("[detection] log stream started: %s/%s", pod.Namespace, pod.Name)
                d.streamPodLogs(streamCtx, pod, repo)
        }()
}

func (d *Detector) streamPodLogs(ctx context.Context, pod *corev1.Pod, repo string) {
        containerName := ""
        if len(pod.Spec.Containers) > 0 {
                containerName = pod.Spec.Containers[0].Name
        }

        opts := &corev1.PodLogOptions{
                Follow:    true,
                Container: containerName,
                TailLines: ptr(int64(100)),
        }

        for {
                select {
                case <-ctx.Done():
                        return
                default:
                }

                req := d.clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts)
                stream, err := req.Stream(ctx)
                if err != nil {
                        select {
                        case <-ctx.Done():
                                return
                        case <-time.After(5 * time.Second):
                        }
                        continue
                }

                scanner := bufio.NewScanner(stream)
                scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

                // v3.2.3: Buffer tracebacks until we see the error type line at the bottom.
                // This prevents firing with "unknown_error" when the actual error type
                // (e.g. KeyError) is at the end of a 40-line traceback.
                var tracebackBuffer strings.Builder
                var inTraceback bool
                var tracebackLines int

                for scanner.Scan() {
                        select {
                        case <-ctx.Done():
                                stream.Close()
                                return
                        default:
                        }

                        line := scanner.Text()

                        // Detect start of a Python traceback
                        if strings.Contains(line, "Traceback (most recent call last)") {
                                inTraceback = true
                                tracebackBuffer.Reset()
                                tracebackLines = 0
                                tracebackBuffer.WriteString(line + "\n")
                                tracebackLines++
                                continue
                        }

                        if inTraceback {
                                tracebackBuffer.WriteString(line + "\n")
                                tracebackLines++

                                // Check if this line is the error type (end of traceback)
                                // e.g. "KeyError: 'DATABASE_URLL'" or "ValueError: invalid"
                                errorType := d.extractErrorType(line)
                                                                isEndOfTraceback := errorType != "unknown_error" && errorType != ""

                                                                // Also check for Go panics, Java exceptions, etc.
                                                                if !isEndOfTraceback {
                                                                        // Check for non-indented line that looks like an error
                                                                        if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") &&
                                                                                (strings.Contains(line, "Error") || strings.Contains(line, "Exception") ||
                                                                                 strings.Contains(line, "panic") || strings.Contains(line, "fatal")) {
                                                                                errorType = d.extractErrorType(line)
                                                                                isEndOfTraceback = errorType != "unknown_error" && errorType != ""
                                                                        }
                                                                }

                                                                if isEndOfTraceback || tracebackLines > 50 {
                                                                        // End of traceback (or buffer overflow) — fire with the correct error type
                                                                        inTraceback = false
                                                                        logs := tracebackBuffer.String()
                                                                        tracebackBuffer.Reset()

                                                                        service := d.getServiceFromPod(pod)
                                                                        if d.shouldTrigger(pod.Namespace, service, errorType) {
                                                                                reason := fmt.Sprintf("Error detected in pod logs: %s", errorType)
                                                                                log.Printf("[detection] log error: %s/%s — %s", pod.Namespace, pod.Name, errorType)
                                                                                d.triggerIncident(pod, repo, reason, logs)
                                                                        }
                                                                }
                                                                continue
                        }

                        // Non-traceback error detection (Go panics, generic errors)
                        if d.isErrorLine(line) {
                                errorType := d.extractErrorType(line)
                                service := d.getServiceFromPod(pod)

                                if d.shouldTrigger(pod.Namespace, service, errorType) {
                                        // Capture next 20 lines for context
                                        var buf strings.Builder
                                        buf.WriteString(line + "\n")
                                        for i := 0; i < 20 && scanner.Scan(); i++ {
                                                buf.WriteString(scanner.Text() + "\n")
                                        }

                                        logs := buf.String()
                                        reason := fmt.Sprintf("Error detected in pod logs: %s", errorType)
                                        log.Printf("[detection] log error: %s/%s — %s", pod.Namespace, pod.Name, errorType)
                                        d.triggerIncident(pod, repo, reason, logs)
                                }
                        }
                }

                stream.Close()

                select {
                case <-ctx.Done():
                        return
                case <-time.After(2 * time.Second):
                }
        }
}

func (d *Detector) isErrorLine(line string) bool {
        for _, p := range errorPatterns {
                if p.MatchString(line) {
                        return true
                }
        }
        return false
}

func (d *Detector) extractErrorType(line string) string {
        patterns := []string{
                `(\w+Error):`,
                `(\w+Exception):`,
                `panic:`,
                `fatal error:`,
                `(\w+Error)\s`,
                `java\.lang\.(\w+)`,
                `thread '.*' panicked`,
        }
        for _, p := range patterns {
                re := regexp.MustCompile(p)
                if m := re.FindStringSubmatch(line); len(m) > 1 {
                        return m[1]
                } else if re.MatchString(line) {
                        return p
                }
        }
        return "unknown_error"
}

func (d *Detector) shouldTrigger(namespace, service, errorType string) bool {
        fp := ErrorFingerprint{Service: service, Namespace: namespace, ErrorType: errorType}

        d.mu.Lock()
        defer d.mu.Unlock()

        if lastSeen, exists := d.recentSeen[fp]; exists {
                if time.Since(lastSeen) < 5*time.Minute {
                        return false
                }
        }
        d.recentSeen[fp] = time.Now()
        return true
}

func (d *Detector) triggerIncident(pod *corev1.Pod, repo, reason, podLogs string) {
        service := d.getServiceFromPod(pod)

        if d.handler != nil {
                go d.handler(service, pod.Namespace, reason, podLogs)
        }

        if d.alertURL != "" {
                go d.sendAlert(service, pod.Namespace, reason, podLogs)
        } else {
                log.Printf("[alert] %s/%s — %s", pod.Namespace, service, reason)
        }
}

func (d *Detector) sendAlert(service, namespace, reason, podLogs string) {
        log.Printf("[alert] %s/%s — %s", namespace, service, reason)
}

func (d *Detector) getServiceFromPod(pod *corev1.Pod) string {
        if name, ok := pod.Labels["app.kubernetes.io/name"]; ok {
                return name
        }
        if name, ok := pod.Labels["app"]; ok {
                return name
        }
        name := pod.Name
        if idx := strings.LastIndex(name, "-"); idx > 0 {
                name = name[:idx]
        }
        return name
}

func (d *Detector) fetchPodLogs(ctx context.Context, pod *corev1.Pod) string {
        limitBytes := int64(8192)
        containerName := ""
        if len(pod.Spec.Containers) > 0 {
                containerName = pod.Spec.Containers[0].Name
        }

        opts := &corev1.PodLogOptions{
                Previous:   true,
                LimitBytes: &limitBytes,
                Container:  containerName,
        }
        if logs, err := d.clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts).Do(ctx).Raw(); err == nil {
                s := string(logs)
                if strings.TrimSpace(s) != "" && !strings.Contains(s, "unable to retrieve container logs") {
                        return s
                }
        }

        opts.Previous = false
        if logs, err := d.clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opts).Do(ctx).Raw(); err == nil {
                return string(logs)
        }

        return ""
}

func (d *Detector) cleanupLoop(ctx context.Context) {
        ticker := time.NewTicker(5 * time.Minute)
        defer ticker.Stop()

        for {
                select {
                case <-ctx.Done():
                        return
                case <-ticker.C:
                        d.mu.Lock()
                        for fp, seen := range d.recentSeen {
                                if time.Since(seen) > 10*time.Minute {
                                        delete(d.recentSeen, fp)
                                }
                        }
                        d.mu.Unlock()
                }
        }
}

func ptr[T any](v T) *T { return &v }
