// Package alertmanager implements an Alertmanager webhook receiver.
//
// Alertmanager sends webhook notifications to a configured URL when an alert
// fires. Sentinel receives these at POST /webhook/alertmanager and converts
// them into internal Signals for the clusterer to deduplicate + group.
//
// Alertmanager webhook payload format (per https://prometheus.io/docs/alerting/latest/configuration/#webhook_config):
//
//	{
//	  "version": "4",
//	  "groupKey": "...",
//	  "status": "firing",
//	  "receiver": "sentinel",
//	  "groupLabels": { "alertname": "HighErrorRate", "service": "payments-api" },
//	  "commonLabels": { "severity": "critical", "service": "payments-api" },
//	  "commonAnnotations": { "summary": "Error rate > 5%", "description": "..." },
//	  "externalURL": "https://alertmanager.example.com",
//	  "alerts": [
//	    {
//	      "status": "firing",
//	      "labels": { "alertname": "HighErrorRate", "service": "payments-api", "namespace": "production" },
//	      "annotations": { "summary": "...", "description": "...", "runbook_url": "..." },
//	      "startsAt": "2024-...",
//	      "fingerprint": "abc123"
//	    }
//	  ]
//	}
//
// Sentinel extracts from each alert:
//   - service: from labels.service (or labels.job)
//   - namespace: from labels.namespace (defaults to "default")
//   - reason: alertname + annotations.summary
//   - repo: from labels.repo (optional — if absent, Sentinel looks it up via K8s annotations)
//   - rawLogs: annotations.description + runbook_url
//
// Only "firing" alerts trigger investigation. "resolved" alerts are logged
// but do not trigger (the original firing alert already started an
// investigation; resolution is handled by the outcome watcher).
package alertmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
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

// SignalHandler is what the Alertmanager source calls when it receives a
// firing alert. Typically the detection.Clusterer implements this.
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
}

// NewReceiver creates an Alertmanager webhook receiver.
func NewReceiver(handler SignalHandler) *Receiver {
	return &Receiver{
		handler:  handler,
		seen:     make(map[string]time.Time),
		cooldown: 5 * time.Minute,
	}
}

// ServeHTTP handles POST /webhook/alertmanager.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload AlertmanagerPayload
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		log.Printf("[alertmanager] failed to decode payload: %v", err)
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

	log.Printf("[alertmanager] received webhook: %d firing, %d resolved (group: %q)",
		firingCount, resolvedCount, payload.GroupKey)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":      "ok",
		"firing":      firingCount,
		"resolved":    resolvedCount,
		"received_at": time.Now().UTC().Format(time.RFC3339),
	})
}

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

func (r *Receiver) processAlert(alert Alert) {
	service := extractService(alert.Labels)
	namespace := alert.Labels["namespace"]
	if namespace == "" {
		namespace = "default"
	}
	reason := buildReason(alert)
	repo := alert.Labels["repo"]
	description := buildDescription(alert)

	log.Printf("[alertmanager] firing alert: service=%s namespace=%s alertname=%s",
		service, namespace, alert.Labels["alertname"])

	r.handler.HandleAlert(service, namespace, reason, repo, description, alert.Labels)
}

func extractService(labels map[string]string) string {
	for _, key := range []string{"service", "job", "deployment", "pod"} {
		if v := labels[key]; v != "" {
			return v
		}
	}
	return "unknown"
}

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
// every 5 minutes. Call once at startup.
func (r *Receiver) StartPurger(ctx context.Context) {
	go func() {
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
