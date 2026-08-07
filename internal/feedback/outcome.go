package feedback

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// fingerprintCache memoizes the compiled regex for each error-fingerprint
// pattern so checkErrorRecurrence does not re-compile on every poll.
var fingerprintCache sync.Map

// OutcomeWatcher monitors a service after a fix is applied (PR opened).
// Two phases:
//
//   - Phase 1 (10 min, polls every 30s): did the service recover? If yes,
//     record a success. If still failing after 10 min, record a failure.
//   - Phase 2 (24h, polls every 5 min): did the ORIGINAL error fingerprint
//     reappear in pod logs? If yes, the fix failed in production — record
//     a failure that overrides the Phase 1 success.
//
// Phase 2 is the regression watcher. Without it, Sentinel would mark a
// fix "successful" the moment pods came back Ready, then never correct
// the Beta prior when the same error resurfaced hours later (common for
// flaky fixes, race conditions, and load-dependent bugs).
type OutcomeWatcher struct {
	clientset kubernetes.Interface
	store     *Store

	shortWatchDuration time.Duration
	shortCheckInterval time.Duration
	longWatchDuration  time.Duration
	longCheckInterval  time.Duration

	// activeWatches dedupes concurrent WatchWithDetails calls for the
	// same incident — the merger and the investigation handler both
	// call Watch, and we only want one watcher per incident.
	activeWatches sync.Map
}

// NewOutcomeWatcher returns an OutcomeWatcher with the default durations
// (Phase 1: 10m / 30s poll; Phase 2: 24h / 5m poll).
func NewOutcomeWatcher(clientset kubernetes.Interface, store *Store) *OutcomeWatcher {
	return &OutcomeWatcher{
		clientset:          clientset,
		store:              store,
		shortWatchDuration: 10 * time.Minute,
		shortCheckInterval: 30 * time.Second,
		longWatchDuration:  24 * time.Hour,
		longCheckInterval:  5 * time.Minute,
	}
}

// SetDurations overrides the Phase 1 and Phase 2 durations. Pass 0 to keep
// the existing value. Useful for tests.
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

// Watch is the legacy Phase-1-only entry point. Prefer WatchWithDetails.
func (w *OutcomeWatcher) Watch(ctx context.Context, service, namespace, incidentID string, prOpened bool) {
	w.WatchWithFixType(ctx, service, namespace, incidentID, prOpened, "code_fix")
}

// WatchWithFixType is the Phase-1 entry point that records the fix type.
func (w *OutcomeWatcher) WatchWithFixType(ctx context.Context, service, namespace, incidentID string, prOpened bool, fixType string) {
	w.WatchWithDetails(ctx, service, namespace, incidentID, prOpened, fixType, "")
}

// WatchWithDetails is the full Phase 1 + Phase 2 watcher. errorFingerprint
// is the error type extracted from the original incident (e.g. "panic",
// "NullPointerException"). If non-empty, Phase 2 watches for its
// re-occurrence in pod logs over 24h. If empty, Phase 2 is skipped.
//
// WatchWithDetails is idempotent per incident: a second call for the same
// service+namespace+incidentID returns without spawning a second goroutine.
func (w *OutcomeWatcher) WatchWithDetails(ctx context.Context, service, namespace, incidentID string, prOpened bool, fixType, errorFingerprint string) {
	watchKey := fmt.Sprintf("%s/%s/%s", service, namespace, incidentID)
	if _, alreadyWatching := w.activeWatches.LoadOrStore(watchKey, true); alreadyWatching {
		logging.Info("outcome: watch already running, skipping duplicate",
			"service", service, "namespace", namespace, "incident_id", incidentID)
		return
	}

	go func() {
		defer w.activeWatches.Delete(watchKey)
		defer func() {
			if r := recover(); r != nil {
				logging.Info("outcome watcher recovered", "r", r)
			}
		}()

		recovered := w.runPhase1(ctx, service, namespace, incidentID, fixType)

		if recovered && errorFingerprint != "" {
			w.runPhase2(ctx, service, namespace, incidentID, fixType, errorFingerprint)
		}
	}()
}

// runPhase1 polls pod health for shortWatchDuration. Returns true if the
// service recovered (all pods Running + Ready, no CrashLoopBackOff).
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
			logging.Info("outcome: did NOT recover within window",
				"namespace", namespace, "service", service,
				"window", w.shortWatchDuration, "checks", checkCount)
			w.recordOutcome(incidentID, service, namespace, false, "did_not_recover", fixType)
			return false
		case <-ticker.C:
			checkCount++
			if w.checkService(ctx, service, namespace) {
				logging.Info("outcome: RECOVERED",
					"namespace", namespace, "service", service, "checks", checkCount)
				w.recordOutcome(incidentID, service, namespace, true, "recovered", fixType)
				return true
			}
		}
	}
}

// runPhase2 watches for error re-occurrence over longWatchDuration. If the
// original error fingerprint reappears in pod logs, the fix is marked
// failed (overriding the Phase 1 success).
func (w *OutcomeWatcher) runPhase2(ctx context.Context, service, namespace, incidentID, fixType, errorFingerprint string) {
	logging.Info("outcome: Phase 2 started — watching for re-occurrence",
		"namespace", namespace, "service", service,
		"fingerprint", errorFingerprint, "window", w.longWatchDuration)

	ticker := time.NewTicker(w.longCheckInterval)
	defer ticker.Stop()

	deadline := time.After(w.longWatchDuration)
	checkCount := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			logging.Info("outcome: Phase 2 complete — no re-occurrence",
				"namespace", namespace, "service", service,
				"fingerprint", errorFingerprint,
				"window", w.longWatchDuration, "checks", checkCount)
			return
		case <-ticker.C:
			checkCount++
			if w.checkErrorRecurrence(ctx, service, namespace, errorFingerprint) {
				logging.Info("outcome: Phase 2 — ERROR RE-OCCURRED! Fix failed in production — updating Beta prior with failure.",
					"namespace", namespace, "service", service,
					"fingerprint", errorFingerprint, "checks", checkCount,
					"elapsed", time.Duration(checkCount)*w.longCheckInterval)
				w.recordOutcome(incidentID, service, namespace, false, "error_reoccurred_24h", fixType)
				return
			}
		}
	}
}

// checkErrorRecurrence fetches the latest 100 lines of pod logs and
// reports whether errorFingerprint appears (case-insensitive substring
// match). Good enough for error types like "KeyError", "panic",
// "NullPointerException" — the fingerprint is specific enough that a
// coincidental substring match is unlikely.
func (w *OutcomeWatcher) checkErrorRecurrence(ctx context.Context, service, namespace, errorFingerprint string) bool {
	logs := w.fetchPodLogs(ctx, service, namespace)
	if logs == "" {
		return false
	}
	return strings.Contains(strings.ToLower(logs), strings.ToLower(errorFingerprint))
}

// fetchPodLogs returns the most recent 100 lines of pod logs (with
// timestamps) from the first pod matching the service. Returns empty
// string on any error.
func (w *OutcomeWatcher) fetchPodLogs(ctx context.Context, service, namespace string) string {
	if w.clientset == nil {
		return ""
	}
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

	pod := &pods.Items[0]
	containerName := ""
	if len(pod.Spec.Containers) > 0 {
		containerName = pod.Spec.Containers[0].Name
	}

	opts := &corev1.PodLogOptions{
		Container:  containerName,
		TailLines:  ptr(int64(100)),
		Timestamps: true,
	}

	req := w.clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return ""
	}
	defer stream.Close()

	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(stream)
	return buf.String()
}

// ExtractErrorFingerprint extracts the error type from an incident's pod
// logs. Returns the first matching error type, or "" if none found.
func ExtractErrorFingerprint(podLogs string) string {
	patterns := []string{
		`(\w+Error):`,
		`(\w+Exception):`,
		`panic:\s*(.+)`,
		`java\.lang\.(\w+(?:Error|Exception))`,
		`TypeError:`,
		`ReferenceError:`,
		`SyntaxError:`,
		`panicked at`,
		`(\w+Error)`,
		`(SIGSEGV|SIGABRT|SIGBUS)`,
	}
	for _, p := range patterns {
		re, _ := fingerprintCache.LoadOrStore(p, regexp.MustCompile(p))
		re2, _ := re.(*regexp.Regexp)
		if m := re2.FindStringSubmatch(podLogs); len(m) >= 2 {
			return m[1]
		}
	}
	return ""
}

// ptr returns a pointer to v — a helper for K8s option structs.
func ptr(i int64) *int64 { return &i }

// checkService reports whether all pods for the service are Running +
// Ready with no CrashLoopBackOff / ImagePullBackOff / ErrImagePull.
func (w *OutcomeWatcher) checkService(ctx context.Context, service, namespace string) bool {
	if w.clientset == nil {
		return false
	}
	pods, err := w.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app.kubernetes.io/name=%s", service),
	})
	if err != nil || len(pods.Items) == 0 {
		pods, err = w.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", service),
		})
		if err != nil || len(pods.Items) == 0 {
			return false
		}
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			return false
		}
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
	return true
}

// recordOutcome writes the outcome to the Beta tracker + the vector store.
// A Phase 2 failure overrides the Phase 1 success: the Beta prior should
// reflect the REAL outcome, not the sandbox outcome.
func (w *OutcomeWatcher) recordOutcome(incidentID, service, namespace string, success bool, detail string, fixType string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, _, err := w.store.RecordBetaOutcome(ctx, service, fixType, success); err != nil {
		logging.Error("outcome: failed to record beta outcome", "err", err)
	}
	if err := w.store.UpdateVectorOutcome(ctx, incidentID, success); err != nil {
		logging.Error("outcome: failed to update vector outcome", "err", err)
	}
	logging.Info("outcome: recorded",
		"namespace", namespace, "service", service,
		"success", success, "fix_type", fixType, "detail", detail)
}
