package alertmanager

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// AlertmanagerPayload is the webhook payload format (v4).
type AlertmanagerPayload struct {
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []Alert           `json:"alerts"`
}

// Alert is a single alert in the Alertmanager payload.
type Alert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt"`
	Fingerprint string            `json:"fingerprint"`
}

// SignalHandler is invoked for each firing alert. Typically implemented by
// detection.Clusterer via a thin adapter.
type SignalHandler interface {
	HandleAlert(service, namespace, reason, repo, description string, labels map[string]string)
}

// Receiver is the Alertmanager webhook receiver. It implements http.Handler
// and should be wired to POST /webhook/alertmanager.
type Receiver struct {
	handler  SignalHandler
	mu       sync.Mutex
	seen     map[string]time.Time
	cooldown time.Duration

	// sharedSecret, if set, requires callers to send it in the
	// X-Sentinel-Webhook-Secret header. Prevents unauthorized alert
	// injection. Configured via WithSharedSecret.
	sharedSecret string
}

// NewReceiver creates an Alertmanager webhook receiver with a 5-minute
// fingerprint cooldown.
func NewReceiver(handler SignalHandler) *Receiver {
	return &Receiver{
		handler:  handler,
		seen:     make(map[string]time.Time),
		cooldown: 5 * time.Minute,
	}
}

// WithSharedSecret requires callers to send the secret in the
// X-Sentinel-Webhook-Secret header. Returns the receiver for chaining.
func (r *Receiver) WithSharedSecret(secret string) *Receiver {
	r.sharedSecret = secret
	return r
}

// SetCooldown overrides the dedup cooldown window. Used by tests that need
// a shorter window than the default 5 minutes.
func (r *Receiver) SetCooldown(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooldown = d
}

// ServeHTTP handles POST /webhook/alertmanager.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Shared-secret auth — prevents unauthorized alert injection. The
	// comparison is constant-time so an attacker cannot time-side-channel
	// the secret byte by byte.
	if r.sharedSecret != "" {
		sent := req.Header.Get("X-Sentinel-Webhook-Secret")
		if subtle.ConstantTimeCompare([]byte(sent), []byte(r.sharedSecret)) != 1 {
			logging.Info("alertmanager: rejected webhook — missing or wrong X-Sentinel-Webhook-Secret")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	var payload AlertmanagerPayload
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		logging.Error("alertmanager: failed to decode payload", "err", err)
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	firingCount := 0
	resolvedCount := 0
	for _, alert := range payload.Alerts {
		if alert.Status == "resolved" {
			resolvedCount++
			continue
		}
		if alert.Status != "firing" {
			continue
		}
		if r.isDuplicate(alert.Fingerprint) {
			continue
		}
		r.processAlert(alert)
		firingCount++
	}

	logging.Info("alertmanager: received webhook",
		"firing", firingCount, "resolved", resolvedCount, "groupKey", payload.GroupKey)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "ok",
		"firing":      firingCount,
		"resolved":    resolvedCount,
		"received_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// isDuplicate reports whether the same fingerprint was seen within the
// cooldown window. Stale entries are purged on each call.
func (r *Receiver) isDuplicate(fingerprint string) bool {
	if fingerprint == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for fp, seen := range r.seen {
		if now.Sub(seen) > r.cooldown {
			delete(r.seen, fp)
		}
	}
	if _, exists := r.seen[fingerprint]; exists {
		return true
	}
	r.seen[fingerprint] = now
	return false
}

// processAlert normalizes an Alert into the service/namespace/reason/repo/
// description/labels tuple and invokes the handler.
func (r *Receiver) processAlert(alert Alert) {
	service := extractService(alert.Labels)
	namespace := alert.Labels["namespace"]
	if namespace == "" {
		namespace = "default"
	}
	reason := buildReason(alert)
	repo := alert.Labels["repo"]
	description := buildDescription(alert)

	logging.Info("alertmanager: firing alert",
		"service", service, "namespace", namespace,
		"alertname", alert.Labels["alertname"])

	r.handler.HandleAlert(service, namespace, reason, repo, description, alert.Labels)
}

// extractService returns the service name from a label set, falling back
// through job/deployment/pod and finally "unknown".
func extractService(labels map[string]string) string {
	for _, key := range []string{"service", "job", "deployment", "pod"} {
		if v := labels[key]; v != "" {
			return v
		}
	}
	return "unknown"
}

// buildReason returns "alertname: summary" or just "alertname" when no
// summary annotation is present.
func buildReason(alert Alert) string {
	alertname := alert.Labels["alertname"]
	if alertname == "" {
		alertname = "Alert"
	}
	summary := alert.Annotations["summary"]
	if summary == "" {
		return alertname
	}
	return fmt.Sprintf("%s: %s", alertname, summary)
}

// buildDescription assembles a multi-line description from the alert's
// annotations, labels, and timing fields. Used as evidence text by the
// investigator.
func buildDescription(alert Alert) string {
	var parts []string
	if d := alert.Annotations["description"]; d != "" {
		parts = append(parts, "Description: "+d)
	}
	if r := alert.Annotations["runbook_url"]; r != "" {
		parts = append(parts, "Runbook: "+r)
	}
	if d := alert.Annotations["dashboard"]; d != "" {
		parts = append(parts, "Dashboard: "+d)
	}
	if len(alert.Labels) > 0 {
		var labelParts []string
		for k, v := range alert.Labels {
			labelParts = append(labelParts, fmt.Sprintf("%s=%s", k, v))
		}
		parts = append(parts, "Labels: "+strings.Join(labelParts, ", "))
	}
	parts = append(parts, "Started: "+alert.StartsAt)
	parts = append(parts, "Fingerprint: "+alert.Fingerprint)
	return strings.Join(parts, "\n")
}

// PurgeOldEntries removes dedup entries older than the cooldown window.
func (r *Receiver) PurgeOldEntries() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for fp, seen := range r.seen {
		if now.Sub(seen) > r.cooldown {
			delete(r.seen, fp)
		}
	}
}

// StartPurger launches a background goroutine that purges old dedup entries
// every 5 minutes. Call once at startup; the goroutine exits when ctx is
// cancelled.
func (r *Receiver) StartPurger(ctx context.Context) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				logging.Info("alertmanager: purger panic recovered", "r", rec)
			}
		}()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.PurgeOldEntries()
			}
		}
	}()
}
