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
type OutcomeWatcher struct {
	clientset *kubernetes.Clientset
	store     *Store
	tracker   interface {
		MarkResolved(service, namespace string, success bool)
	}
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
func (w *OutcomeWatcher) Watch(ctx context.Context, service, namespace, incidentID string, prOpened bool) {
	go func() {
		// Wait 60s for the PR to be merged + deployed (in autonomous mode, the human
		// merges the PR; we check if the pod recovers within 10 min)
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
					w.recordOutcome(incidentID, service, namespace, false, "did_not_recover")
				}
				return
			case <-ticker.C:
				checkCount++
				recovered = w.checkService(ctx, service, namespace)
				if recovered {
					log.Printf("[outcome] %s/%s — RECOVERED after %d checks (%.1f min)", namespace, service, checkCount, float64(checkCount)*0.5)
					w.recordOutcome(incidentID, service, namespace, true, "recovered")
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
func (w *OutcomeWatcher) recordOutcome(incidentID, service, namespace string, success bool, detail string) {
	ctx := context.Background()

	// Update Beta tracker
	_, _, err := w.store.RecordBetaOutcome(ctx, service, "code_fix", success)
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

	log.Printf("[outcome] recorded: %s/%s success=%v (%s)", namespace, service, success, detail)
}
