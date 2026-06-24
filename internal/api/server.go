// Package api provides the HTTP API for Sentinel v3.
// Replaces the FastAPI app in v2 + the Next.js UI pod.
// Routes:
//   GET  /healthz        — liveness
//   GET  /readyz         — readiness (DB + watcher running)
//   GET  /metrics        — Prometheus metrics
//   GET  /incidents      — list recent incidents
//   GET  /incidents/{id} — single incident
//   GET  /metrics-summary — dashboard summary
//   GET  /settings       — runtime settings (masked)
//   POST /settings       — update settings
//   POST /admin/investigate — manually trigger an investigation
package api

import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "net/http"
        "strings"
        "sync"
        "time"

        "github.com/google/uuid"

        "github.com/karimzakzouk/sentinel/v3/internal/metrics"
)

// Server is the HTTP API server.
type Server struct {
        store           Store
        settings        SettingsGetter
        investigate     InvestigateFunc
        readyChecker    ReadyChecker
        webhookHandler  http.Handler
        slackHandler    http.Handler
        pauseController PauseController
        webUIHandler    http.Handler
        http            *http.Server

        // v3.7.0: SSE event broadcasting
        eventSubs   map[chan ServerEvent]bool
        eventSubsMu sync.Mutex
}

// ServerEvent is an event pushed to connected SSE clients.
type ServerEvent struct {
        Type string      `json:"type"` // "incident", "log", "metric", "status"
        Data interface{} `json:"data"`
}

// BroadcastEvent pushes an event to all connected SSE clients.
// v3.7.0: Called from the investigation handler to push real-time updates.
func (s *Server) BroadcastEvent(eventType string, data interface{}) {
        s.eventSubsMu.Lock()
        defer s.eventSubsMu.Unlock()
        for ch := range s.eventSubs {
                select {
                case ch <- ServerEvent{Type: eventType, Data: data}:
                default: // drop if client is slow
                }
        }
}

// PauseController controls autonomous mode pause/resume.
// v3.6.0: Implemented by the Slack bot — allows the CLI to pause via the API.
type PauseController interface {
        IsPaused() bool
        Pause(duration time.Duration, reason string)
        Resume()
}

// Store interface — satisfied by feedback.Store.
type Store interface {
        ListRecentIncidents(ctx context.Context, limit int) ([]any, error)
        GetIncident(ctx context.Context, id uuid.UUID) (any, error)
}

// SettingsGetter returns current runtime settings (masked).
type SettingsGetter func() map[string]any

// InvestigateFunc manually triggers an investigation.
type InvestigateFunc func(service, namespace, reason string) (string, error)

// ReadyChecker returns true when the system is ready to serve.
type ReadyChecker func() bool

// New creates a new API server.
func New(addr string, store Store, settings SettingsGetter, investigate InvestigateFunc, ready ReadyChecker) *Server {
        s := &Server{
                store:        store,
                settings:     settings,
                investigate:  investigate,
                readyChecker: ready,
                eventSubs:    make(map[chan ServerEvent]bool),
        }
        mux := http.NewServeMux()
        mux.HandleFunc("/healthz", s.handleHealthz)
        mux.HandleFunc("/readyz", s.handleReadyz)
        mux.HandleFunc("/metrics", s.handleMetrics)
        mux.HandleFunc("/metrics-summary", s.handleMetricsSummary)
        mux.HandleFunc("/incidents", s.handleIncidents)
        mux.HandleFunc("/incidents/", s.handleIncident)
        mux.HandleFunc("/settings", s.handleSettings)
        mux.HandleFunc("/admin/investigate", s.handleInvestigate)
        // v3.6.0: CLI pause/resume via API
        mux.HandleFunc("/admin/pause", s.handlePause)
        mux.HandleFunc("/admin/resume", s.handleResume)
        // v3.7.0: Server-Sent Events for real-time UI updates
        mux.HandleFunc("/events", s.handleSSE)
        // v3.5.0: Predictive PR review webhook receiver.
        // webhookHandler is nil by default — set via SetWebhookHandler.
        mux.HandleFunc("/webhook/github", s.handleWebhook)
        // v3.5.0: Interactive Slack bot (Events API + slash commands).
        mux.HandleFunc("/slack/events", s.handleSlack)
        // v3.6.0: Web UI — served from embedded files (internal/web)
        // The handler is set via SetWebUIHandler. If nil, / returns a placeholder.
        mux.HandleFunc("/", s.handleWebUI)

        s.http = &http.Server{
                Addr:    addr,
                Handler: mux,
        }
        return s
}

// SetWebhookHandler wires in the GitHub webhook handler (predictive PR review).
// v3.5.0: If not called, /webhook/github returns 503 Service Unavailable.
func (s *Server) SetWebhookHandler(h http.Handler) {
        s.webhookHandler = h
}

// SetSlackHandler wires in the Slack bot handler (interactive commands).
// v3.5.0: If not called, /slack/events returns 503 Service Unavailable.
func (s *Server) SetSlackHandler(h http.Handler) {
        s.slackHandler = h
}

// SetPauseController wires in the pause controller (Slack bot) for CLI access.
// v3.6.0: Allows sentinelctl pause/resume to work via the API.
func (s *Server) SetPauseController(pc PauseController) {
        s.pauseController = pc
}

// SetWebUIHandler wires in the embedded web UI handler.
// v3.6.0: Serves the Next.js dashboard at /.
func (s *Server) SetWebUIHandler(h http.Handler) {
        s.webUIHandler = h
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
                writeJSON(w, 405, map[string]string{"error": "method not allowed"})
                return
        }
        if s.pauseController == nil {
                writeJSON(w, 503, map[string]string{"error": "pause controller not configured"})
                return
        }
        var req struct {
                DurationMin int    `json:"duration_min"`
                Reason      string `json:"reason"`
        }
        if r.Body != nil {
                _ = json.NewDecoder(r.Body).Decode(&req)
        }
        if req.DurationMin <= 0 {
                req.DurationMin = 30
        }
        if req.Reason == "" {
                req.Reason = "paused via CLI"
        }
        s.pauseController.Pause(time.Duration(req.DurationMin)*time.Minute, req.Reason)
        writeJSON(w, 200, map[string]any{
                "status":       "paused",
                "duration_min": req.DurationMin,
                "reason":       req.Reason,
        })
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
                writeJSON(w, 405, map[string]string{"error": "method not allowed"})
                return
        }
        if s.pauseController == nil {
                writeJSON(w, 503, map[string]string{"error": "pause controller not configured"})
                return
        }
        s.pauseController.Resume()
        writeJSON(w, 200, map[string]string{"status": "resumed"})
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
        if s.webhookHandler == nil {
                writeJSON(w, 503, map[string]string{
                        "error": "webhook handler not configured (predictive PR review disabled)",
                })
                return
        }
        s.webhookHandler.ServeHTTP(w, r)
}

func (s *Server) handleSlack(w http.ResponseWriter, r *http.Request) {
        if s.slackHandler == nil {
                writeJSON(w, 503, map[string]string{
                        "error": "slack handler not configured (interactive bot disabled)",
                })
                return
        }
        s.slackHandler.ServeHTTP(w, r)
}

func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request) {
        if s.webUIHandler == nil {
                w.Header().Set("Content-Type", "text/html; charset=utf-8")
                w.Write([]byte(`<!DOCTYPE html><html><head><title>Sentinel</title></head><body style="background:#0A0A0B;color:#A1A1AA;font-family:monospace;padding:40px;"><h1 style="color:#06B6D4;">Sentinel</h1><p>Web UI not built. The API is running at <code>/healthz</code>, <code>/incidents</code>, <code>/metrics</code>.</p></body></html>`))
                return
        }
        s.webUIHandler.ServeHTTP(w, r)
}

// handleSSE handles Server-Sent Events connections for real-time UI updates.
// v3.7.0: The UI connects to /events and receives push notifications when
// incidents are detected, pipeline steps complete, PRs open, etc.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        w.Header().Set("Cache-Control", "no-cache")
        w.Header().Set("Connection", "keep-alive")
        w.Header().Set("Access-Control-Allow-Origin", "*")

        flusher, ok := w.(http.Flusher)
        if !ok {
                http.Error(w, "streaming not supported", http.StatusInternalServerError)
                return
        }

        // Register this client
        ch := make(chan ServerEvent, 16)
        s.eventSubsMu.Lock()
        s.eventSubs[ch] = true
        s.eventSubsMu.Unlock()
        defer func() {
                s.eventSubsMu.Lock()
                delete(s.eventSubs, ch)
                s.eventSubsMu.Unlock()
        }()

        // Send initial connection event
        fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\"}\n\n")
        flusher.Flush()

        // Ping ticker (keep-alive every 15s)
        ticker := time.NewTicker(15 * time.Second)
        defer ticker.Stop()

        for {
                select {
                case <-r.Context().Done():
                        return
                case event := <-ch:
                        data, _ := json.Marshal(event)
                        fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
                        flusher.Flush()
                case <-ticker.C:
                        fmt.Fprintf(w, ": ping\n\n")
                        flusher.Flush()
                }
        }
}

// Start begins serving HTTP.
func (s *Server) Start() error {
        log.Printf("[api] listening on %s", s.http.Addr)
        return s.http.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
        return s.http.Shutdown(ctx)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, 200, map[string]string{
                "status":  "ok",
                "service": "core",
                "version": "3.7.0",
        })
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
        ready := s.readyChecker()
        status := "not_ready"
        if ready {
                status = "ready"
        }
        writeJSON(w, 200, map[string]any{
                "status": status,
                "ready":  ready,
        })
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
        // v3.4.0: Real Prometheus metrics — was a hardcoded stub returning 0.
        // Now exports counters/histograms/gauges for investigation count, duration,
        // LLM latency, sandbox pass rate, PR count, active incidents gauge.
        metrics.Handler().ServeHTTP(w, r)
}

func (s *Server) handleMetricsSummary(w http.ResponseWriter, r *http.Request) {
        incidents, _ := s.store.ListRecentIncidents(r.Context(), 100)
        active, resolved, failed := 0, 0, 0
        for _, inc := range incidents {
                m, ok := inc.(map[string]any)
                if !ok {
                        continue
                }
                status, _ := m["status"].(string)
                switch {
                case strings.Contains("investigating root_caused fix_proposed approved executing", status):
                        active++
                case strings.Contains("verified resolved", status):
                        resolved++
                case strings.Contains("failed rejected escalated", status):
                        failed++
                }
        }
        writeJSON(w, 200, map[string]int{
                "active":       active,
                "resolved_24h": resolved,
                "failed":       failed,
                "total":        len(incidents),
        })
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
        limit := 50
        if l := r.URL.Query().Get("limit"); l != "" {
                if n, err := fmt.Sscanf(l, "%d", &limit); err != nil || n == 0 {
                        limit = 50
                }
        }
        incidents, err := s.store.ListRecentIncidents(r.Context(), limit)
        if err != nil {
                writeJSON(w, 500, map[string]string{"error": err.Error()})
                return
        }
        writeJSON(w, 200, incidents)
}

func (s *Server) handleIncident(w http.ResponseWriter, r *http.Request) {
        idStr := strings.TrimPrefix(r.URL.Path, "/incidents/")
        id, err := uuid.Parse(idStr)
        if err != nil {
                writeJSON(w, 400, map[string]string{"error": "invalid incident ID"})
                return
        }
        // v3.3.1: Implemented — was returning 404 "not implemented yet" despite
        // store.GetIncident() existing since v3.0.
        incident, err := s.store.GetIncident(r.Context(), id)
        if err != nil {
                writeJSON(w, 500, map[string]string{"error": err.Error()})
                return
        }
        if incident == nil {
                writeJSON(w, 404, map[string]string{"error": "incident not found"})
                return
        }
        writeJSON(w, 200, incident)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
        if r.Method == "GET" {
                writeJSON(w, 200, s.settings())
                return
        }
        // POST — update settings (not yet implemented — runtime settings are env-var only)
        writeJSON(w, 501, map[string]string{"error": "settings update not implemented — use env vars"})
}

func (s *Server) handleInvestigate(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
                writeJSON(w, 405, map[string]string{"error": "method not allowed"})
                return
        }
        var spec struct {
                Service   string `json:"service"`
                Namespace string `json:"namespace"`
                Reason    string `json:"reason"`
        }
        if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
                writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
                return
        }
        if spec.Service == "" {
                writeJSON(w, 400, map[string]string{"error": "service is required"})
                return
        }
        if spec.Namespace == "" {
                spec.Namespace = "default"
        }
        if spec.Reason == "" {
                spec.Reason = "manual trigger"
        }

        clusterID, err := s.investigate(spec.Service, spec.Namespace, spec.Reason)
        if err != nil {
                writeJSON(w, 500, map[string]string{"error": err.Error()})
                return
        }
        writeJSON(w, 200, map[string]string{
                "status":     "triggered",
                "service":    spec.Service,
                "cluster_id": clusterID,
        })
}

func writeJSON(w http.ResponseWriter, status int, v any) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        _ = json.NewEncoder(w).Encode(v)
}
