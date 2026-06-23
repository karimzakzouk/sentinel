package feedback

import (
        "bytes"
        "context"
        "fmt"
        "log"
        "regexp"
        "strings"
        "time"

        corev1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/client-go/kubernetes"
)

// OutcomeWatcher monitors a service after a fix is applied (PR opened).
//
// Two-phase watching:
//   Phase 1 (10 min): polls pod health every 30s. If pods recover → success.
//                     If still failing after 10 min → failure.
//   Phase 2 (24h):    long-term re-occurrence watch. Every 5 min, fetches pod
//                     logs and checks if the ORIGINAL error fingerprint
//                     reappears. If it does → the fix failed in production
//                     → update Beta prior with a failure (overrides the
//                     Phase 1 success if any).
//
// v3.5.0: Added Phase 2 — the 24-hour re-occurrence watcher. Before this,
// Sentinel marked a fix as "successful" if the pod was Running+Ready 10
// minutes after the PR opened. But if the same error reappeared 2 hours
// later (common for flaky fixes, race conditions, load-dependent bugs),
// the Beta prior was never corrected. The system "learned" from sandbox
// results, not real outcomes.
//
// v3.3.1: Removed dead `tracker` field. Added panic recovery. fixType parameter.
type OutcomeWatcher struct {
        clientset *kubernetes.Clientset
        store     *Store
        // v3.5.0: configurable durations (were hardcoded)
        shortWatchDuration time.Duration // Phase 1: default 10 min
        shortCheckInterval time.Duration // Phase 1 poll: default 30s
        longWatchDuration  time.Duration // Phase 2: default 24h
        longCheckInterval  time.Duration // Phase 2 poll: default 5 min
}

// NewOutcomeWatcher creates an OutcomeWatcher with default durations.
func NewOutcomeWatcher(clientset *kubernetes.Clientset, store *Store) *OutcomeWatcher {
        return &OutcomeWatcher{
                clientset:          clientset,
                store:              store,
                shortWatchDuration: 10 * time.Minute,
                shortCheckInterval: 30 * time.Second,
                longWatchDuration:  24 * time.Hour,
                longCheckInterval:  5 * time.Minute,
        }
}

// SetDurations configures both Phase 1 and Phase 2 durations.
// v3.5.0: Allows tests to use short durations. Production uses defaults.
func (w *OutcomeWatcher) SetDurations(shortWatch, shortInterval, longWatch, longInterval time.Duration) {
        if shortWatch > 0 {
                w.shortWatchDuration = shortWatch
        }
        if shortInterval > 0 {
                w.shortCheckInterval = shortInterval
        }
        if longWatch > 0 {
                w.longWatchDuration = longWatch
        }
        if longInterval > 0 {
                w.longCheckInterval = longInterval
        }
}

// Watch monitors the service for 10 minutes after a fix (Phase 1 only).
// v3.3.1: fixType is passed through to recordOutcome.
func (w *OutcomeWatcher) Watch(ctx context.Context, service, namespace, incidentID string, prOpened bool) {
        w.WatchWithFixType(ctx, service, namespace, incidentID, prOpened, "code_fix")
}

// WatchWithFixType is the Phase 1 watcher that records the actual fix type.
func (w *OutcomeWatcher) WatchWithFixType(ctx context.Context, service, namespace, incidentID string, prOpened bool, fixType string) {
        w.WatchWithDetails(ctx, service, namespace, incidentID, prOpened, fixType, "")
}

// WatchWithDetails is the full watcher (Phase 1 + Phase 2).
// v3.5.0: errorFingerprint is the original error type (e.g. "KeyError",
// "panic", "NullPointerException"). If non-empty, Phase 2 will watch for
// its re-occurrence in pod logs over 24h. If empty, Phase 2 is skipped.
func (w *OutcomeWatcher) WatchWithDetails(ctx context.Context, service, namespace, incidentID string, prOpened bool, fixType, errorFingerprint string) {
        go func() {
                defer func() {
                        if r := recover(); r != nil {
                                log.Printf("[panic] outcome watcher recovered: %v", r)
                        }
                }()

                // === Phase 1: Short-term recovery (10 min) ===
                recovered := w.runPhase1(ctx, service, namespace, incidentID, fixType)

                // === Phase 2: Long-term re-occurrence watch (24h) ===
                // v3.5.0: Only run if we have an error fingerprint AND the service
                // recovered in Phase 1 (if it didn't recover, we already recorded
                // failure — no point watching for re-occurrence).
                if recovered && errorFingerprint != "" {
                        w.runPhase2(ctx, service, namespace, incidentID, fixType, errorFingerprint)
                }
        }()
}

// runPhase1 polls pod health for shortWatchDuration. Returns true if recovered.
func (w *OutcomeWatcher) runPhase1(ctx context.Context, service, namespace, incidentID, fixType string) bool {
        ticker := time.NewTicker(w.shortCheckInterval)
        defer ticker.Stop()

        deadline := time.After(w.shortWatchDuration)
        checkCount := 0

        for {
                select {
                case <-ctx.Done():
                        return false
                case <-deadline:
                        log.Printf("[outcome] %s/%s — did NOT recover within %v (checks: %d)",
                                namespace, service, w.shortWatchDuration, checkCount)
                        w.recordOutcome(incidentID, service, namespace, false, "did_not_recover", fixType)
                        return false
                case <-ticker.C:
                        checkCount++
                        if w.checkService(ctx, service, namespace) {
                                log.Printf("[outcome] %s/%s — RECOVERED after %d checks",
                                        namespace, service, checkCount)
                                w.recordOutcome(incidentID, service, namespace, true, "recovered", fixType)
                                return true
                        }
                }
        }
}

// runPhase2 watches for error re-occurrence over 24h.
// v3.5.0: Every longCheckInterval, fetches pod logs and checks if the
// original error fingerprint reappears. If it does → fix failed in prod →
// record a failure (overriding the Phase 1 success).
func (w *OutcomeWatcher) runPhase2(ctx context.Context, service, namespace, incidentID, fixType, errorFingerprint string) {
        log.Printf("[outcome] %s/%s — Phase 2 started: watching for '%s' re-occurrence over %v",
                namespace, service, errorFingerprint, w.longWatchDuration)

        ticker := time.NewTicker(w.longCheckInterval)
        defer ticker.Stop()

        deadline := time.After(w.longWatchDuration)
        checkCount := 0

        for {
                select {
                case <-ctx.Done():
                        return
                case <-deadline:
                        log.Printf("[outcome] %s/%s — Phase 2 complete: no re-occurrence of '%s' in %v (checks: %d)",
                                namespace, service, errorFingerprint, w.longWatchDuration, checkCount)
                        return
                case <-ticker.C:
                        checkCount++
                        if w.checkErrorRecurrence(ctx, service, namespace, errorFingerprint) {
                                log.Printf("[outcome] %s/%s — Phase 2: ERROR RE-OCCURRED! '%s' found in pod logs after %d checks (~%v). "+
                                        "Fix failed in production — updating Beta prior with failure.",
                                        namespace, service, errorFingerprint, checkCount,
                                        time.Duration(checkCount)*w.longCheckInterval)
                                w.recordOutcome(incidentID, service, namespace, false, "error_reoccurred_24h", fixType)
                                return
                        }
                }
        }
}

// checkErrorRecurrence fetches recent pod logs and checks if the error
// fingerprint appears. v3.5.0: Uses the K8s API to get current (non-previous)
// logs with a 100-line tail. The fingerprint match is case-insensitive
// substring search — good enough for error types like "KeyError", "panic",
// "NullPointerException".
func (w *OutcomeWatcher) checkErrorRecurrence(ctx context.Context, service, namespace, errorFingerprint string) bool {
        logs := w.fetchPodLogs(ctx, service, namespace)
        if logs == "" {
                return false // can't fetch logs — can't confirm re-occurrence
        }
        return strings.Contains(strings.ToLower(logs), strings.ToLower(errorFingerprint))
}

// fetchPodLogs fetches current pod logs (100-line tail) for the service.
func (w *OutcomeWatcher) fetchPodLogs(ctx context.Context, service, namespace string) string {
        // Find pods for the service
        pods, err := w.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
                LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
        })
        if err != nil || len(pods.Items) == 0 {
                pods, err = w.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
                        LabelSelector: fmt.Sprintf("app=%s", service),
                })
                if err != nil || len(pods.Items) == 0 {
                        return ""
                }
        }

        // Fetch logs from the first pod (good enough for re-occurrence check)
        pod := &pods.Items[0]
        containerName := ""
        if len(pod.Spec.Containers) > 0 {
                containerName = pod.Spec.Containers[0].Name
        }

        opts := &corev1.PodLogOptions{
                Container:  containerName,
                TailLines:  ptr(int64(100)),
                Timestamps: true, // so we can see if the error is recent
        }

        req := w.clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, opts)
        stream, err := req.Stream(ctx)
        if err != nil {
                return ""
        }
        defer stream.Close()

        buf := new(bytes.Buffer)
        buf.ReadFrom(stream)
        return buf.String()
}

// extractErrorFingerprint extracts the error type from an incident's pod logs.
// v3.5.0: Used by main.go to pass the fingerprint to WatchWithDetails.
// Returns the first matching error type, or "" if none found.
func ExtractErrorFingerprint(podLogs string) string {
        // Common error type patterns across languages
        patterns := []string{
                // Python
                `(\w+Error):`, `(\w+Exception):`,
                // Go
                `panic:\s*(.+)`,
                // Java
                `java\.lang\.(\w+(?:Error|Exception))`,
                // JavaScript
                `TypeError:`, `ReferenceError:`, `SyntaxError:`,
                // Rust
                `panicked at`,
                // Ruby
                `(\w+Error)`,
                // Signals
                `(SIGSEGV|SIGABRT|SIGBUS)`,
        }
        for _, p := range patterns {
                re := regexp.MustCompile(p)
                if m := re.FindStringSubmatch(podLogs); len(m) >= 2 {
                        return m[1]
                }
        }
        return ""
}

func ptr(i int64) *int64 { return &i }

// checkService checks if the service's pods are healthy.
func (w *OutcomeWatcher) checkService(ctx context.Context, service, namespace string) bool {
        pods, err := w.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
                LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
        })
        if err != nil || len(pods.Items) == 0 {
                // Try app label
                pods, err = w.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
                        LabelSelector: fmt.Sprintf("app=%s", service),
                })
                if err != nil || len(pods.Items) == 0 {
                        return false // can't find pods — not recovered
                }
        }

        for _, pod := range pods.Items {
                // Check pod phase
                if pod.Status.Phase != corev1.PodRunning {
                        return false
                }
                // Check container statuses
                for _, cs := range pod.Status.ContainerStatuses {
                        if cs.State.Waiting != nil {
                                if cs.State.Waiting.Reason == "CrashLoopBackOff" ||
                                        cs.State.Waiting.Reason == "ImagePullBackOff" ||
                                        cs.State.Waiting.Reason == "ErrImagePull" {
                                        return false
                                }
                        }
                        if !cs.Ready {
                                return false
                        }
                }
        }

        return true // all pods Running + Ready
}

// recordOutcome writes the outcome to the Beta tracker + vector store.
// v3.3.1: fixType is now a parameter (was hardcoded to "code_fix").
// v3.5.0: Called twice if Phase 2 detects re-occurrence — the second call
// overrides the first (success → failure). This is correct: the Beta prior
// should reflect the REAL outcome, not the sandbox outcome.
func (w *OutcomeWatcher) recordOutcome(incidentID, service, namespace string, success bool, detail string, fixType string) {
        ctx := context.Background()

        // Update Beta tracker
        _, _, err := w.store.RecordBetaOutcome(ctx, service, fixType, success)
        if err != nil {
                log.Printf("[outcome] failed to record beta outcome: %v", err)
        }

        // Update the incident's outcome in the vector store
        _, err = w.store.db.ExecContext(ctx,
                `UPDATE incident_vectors SET outcome_verified = 1, outcome_success = ? WHERE incident_id = ?`,
                boolToInt(success), incidentID)
        if err != nil {
                log.Printf("[outcome] failed to update vector outcome: %v", err)
        }

        log.Printf("[outcome] recorded: %s/%s success=%v fix_type=%s (%s)", namespace, service, success, fixType, detail)
}
