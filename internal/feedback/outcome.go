package feedback

import (
        "context"
        "fmt"
        "log"
        "time"

        corev1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/client-go/kubernetes"
)

// OutcomeWatcher monitors a service after a fix is applied (PR opened).
// Watches for 10 minutes to see if the service recovers.
// This closes the feedback loop — we learn if the fix ACTUALLY worked.
//
// v3.3.1: Removed dead `tracker` field (was declared but never assigned or used).
// v3.3.1: Added panic recovery to the watcher goroutine.
// v3.3.1: recordOutcome now accepts the actual fix_type instead of hardcoding "code_fix".
type OutcomeWatcher struct {
        clientset *kubernetes.Clientset
        store     *Store
}

// NewOutcomeWatcher creates an OutcomeWatcher.
func NewOutcomeWatcher(clientset *kubernetes.Clientset, store *Store) *OutcomeWatcher {
        return &OutcomeWatcher{
                clientset: clientset,
                store:     store,
        }
}

// Watch monitors the service for 10 minutes after a fix.
// If the service recovers → record success.
// If still failing → record failure.
//
// v3.3.1: fixType is now passed through to recordOutcome so the Beta prior
// is updated against the correct fix category (was hardcoded to "code_fix").
func (w *OutcomeWatcher) Watch(ctx context.Context, service, namespace, incidentID string, prOpened bool) {
        w.WatchWithFixType(ctx, service, namespace, incidentID, prOpened, "code_fix")
}

// WatchWithFixType is the v3.3.1 watcher that records the actual fix type
// in the Beta prior. Callers that know the fix type should use this directly.
func (w *OutcomeWatcher) WatchWithFixType(ctx context.Context, service, namespace, incidentID string, prOpened bool, fixType string) {
        go func() {
                // v3.3.1: Panic recovery — without this, a panic in checkService
                // or recordOutcome would kill the watcher goroutine silently.
                defer func() {
                        if r := recover(); r != nil {
                                log.Printf("[panic] outcome watcher recovered: %v", r)
                        }
                }()

                watchDuration := 10 * time.Minute
                checkInterval := 30 * time.Second

                ticker := time.NewTicker(checkInterval)
                defer ticker.Stop()

                deadline := time.After(watchDuration)
                checkCount := 0
                recovered := false

                for {
                        select {
                        case <-ctx.Done():
                                return
                        case <-deadline:
                                if !recovered {
                                        log.Printf("[outcome] %s/%s — did NOT recover within 10 min (checks: %d)", namespace, service, checkCount)
                                        w.recordOutcome(incidentID, service, namespace, false, "did_not_recover", fixType)
                                }
                                return
                        case <-ticker.C:
                                checkCount++
                                recovered = w.checkService(ctx, service, namespace)
                                if recovered {
                                        log.Printf("[outcome] %s/%s — RECOVERED after %d checks (%.1f min)", namespace, service, checkCount, float64(checkCount)*0.5)
                                        w.recordOutcome(incidentID, service, namespace, true, "recovered", fixType)
                                        return
                                }
                        }
                }
        }()
}

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
// v3.3.1: fixType is now a parameter (was hardcoded to "code_fix", which
// meant restart/scale/rollback fixes never updated their own Beta priors).
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
