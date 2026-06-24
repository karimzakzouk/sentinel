// Package api provides the HTTP API server for Sentinel.
package api

import (
        "context"
        "crypto/subtle"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "net/http"
        "strings"
        "sync"
        "time"

        "github.com/google/uuid"

        "github.com/karimzakzouk/sentinel/v3/internal/metrics"
)

// ServerEvent is pushed to SSE clients.
type ServerEvent struct {
        Type string      `json:"type"`
        Data interface{} `json:"data"`
}

// RuntimeCfgSaver saves runtime config (implemented by runtimecfg.Manager).
type RuntimeCfgSaver interface {
        SaveFromJSON(jsonBytes []byte) error
}

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

        // v3.7.0: SSE
        eventSubs   map[chan ServerEvent]bool
        eventSubsMu sync.Mutex

        // v3.8.0: Runtime config persistence
        rtCfgSaver RuntimeCfgSaver

        // v3.8.4: Bearer token required for admin endpoints. Empty = auth
        // disabled (the server logs a loud warning at startup). When set,
        // every request to /admin/* or POST /settings must carry
        // 'Authorization: Bearer <token>'. Health/readiness/metrics/SSE/web UI
        // remain unauthenticated so dashboards and probes still work.
        apiToken string
}

type Store interface {
        ListRecentIncidents(ctx context.Context, limit int) ([]any, error)
        GetIncident(ctx context.Context, id uuid.UUID) (any, error)
        // v3.8.4: FindIncidentByPrefix supports short-ID lookup. The CLI
        // displays 8-char truncated IDs in `incidents list` output; without
        // this, `incidents get <8-char-id>` fails because GetIncident requires
        // a full UUID. Returns nil (no error) if no match; prefix must be ≥4
        // chars to prevent accidental collisions.
        FindIncidentByPrefix(ctx context.Context, prefix string) (any, error)
}

type SettingsGetter func() map[string]any
type InvestigateFunc func(service, namespace, reason string) (string, error)
type ReadyChecker func() bool

type PauseController interface {
        IsPaused() bool
        Pause(duration time.Duration, reason string)
        Resume()
}

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
        mux.HandleFunc("/admin/pause", s.handlePause)
        mux.HandleFunc("/admin/resume", s.handleResume)
        mux.HandleFunc("/events", s.handleSSE)
        mux.HandleFunc("/webhook/github", s.handleWebhook)
        mux.HandleFunc("/slack/events", s.handleSlack)
        mux.HandleFunc("/", s.handleWebUI)

        // v3.8.4: Wrap the mux in an auth middleware that gates admin endpoints
        // on a bearer token. The middleware is a no-op when apiToken is empty
        // (set via SetAPIToken), but we still register it so the wrapping order
        // is consistent.
        s.http = &http.Server{Addr: addr, Handler: s.authMiddleware(mux)}
        return s
}

// SetAPIToken enables bearer-token auth on admin endpoints. Empty = disabled.
// v3.8.4: When set, every request to /admin/* or POST /settings must carry
// 'Authorization: Bearer <token>'. Mismatched or missing tokens get a 401.
// Health/readiness/metrics/SSE/web UI remain unauthenticated so dashboards
// and probes still work.
func (s *Server) SetAPIToken(token string) {
        s.apiToken = token
        if token == "" {
                log.Printf("[api] WARNING: SENTINEL_API_TOKEN is empty — admin API is unauthenticated. Set it in production to prevent unauthorized investigation triggers, pause/resume, and settings changes.")
        } else {
                log.Printf("[api] admin API auth enabled (bearer token set)")
        }
}

// authMiddleware wraps the inner handler with bearer-token auth for admin
// endpoints. Non-admin endpoints pass through unchanged. When apiToken is
// empty, all requests pass through (with a startup warning logged).
//
// Admin endpoints (require auth when apiToken is set):
//   - /admin/investigate (POST)
//   - /admin/pause        (POST)
//   - /admin/resume       (POST)
//   - /settings           (POST only — GET is read-only)
//
// Webhook and Slack endpoints do their own signature verification and are
// not gated by this middleware.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                if s.apiToken == "" {
                        next.ServeHTTP(w, r)
                        return
                }

                isAdmin := strings.HasPrefix(r.URL.Path, "/admin/")
                isSettingsWrite := r.URL.Path == "/settings" && r.Method == http.MethodPost

                if !isAdmin && !isSettingsWrite {
                        next.ServeHTTP(w, r)
                        return
                }

                // Require bearer token.
                authHeader := r.Header.Get("Authorization")
                const prefix = "Bearer "
                if !strings.HasPrefix(authHeader, prefix) {
                        writeJSON(w, 401, map[string]string{
                                "error": "missing or malformed Authorization header (expected 'Bearer <token>')",
                        })
                        return
                }
                provided := authHeader[len(prefix):]
                // Constant-time comparison to prevent timing attacks on the token.
                if !secureCompare(provided, s.apiToken) {
                        writeJSON(w, 401, map[string]string{"error": "invalid token"})
                        return
                }

                next.ServeHTTP(w, r)
        })
}

// secureCompare does a constant-time string comparison so that an attacker
// can't time-side-channel the token byte-by-byte. Wraps
// crypto/subtle.ConstantTimeCompare, returning false on length mismatch
// (which is technically a length leak but not a content leak — acceptable
// for bearer tokens where the length is already obvious from the header).
func secureCompare(a, b string) bool {
        if len(a) != len(b) {
                return false
        }
        return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) SetWebhookHandler(h http.Handler)     { s.webhookHandler = h }
func (s *Server) SetSlackHandler(h http.Handler)       { s.slackHandler = h }
func (s *Server) SetPauseController(pc PauseController) { s.pauseController = pc }
func (s *Server) SetWebUIHandler(h http.Handler)       { s.webUIHandler = h }
func (s *Server) SetRuntimeCfgSaver(r RuntimeCfgSaver)  { s.rtCfgSaver = r }

// BroadcastEvent pushes an event to all connected SSE clients.
func (s *Server) BroadcastEvent(eventType string, data interface{}) {
        s.eventSubsMu.Lock()
        defer s.eventSubsMu.Unlock()
        for ch := range s.eventSubs {
                select {
                case ch <- ServerEvent{Type: eventType, Data: data}:
                default:
                }
        }
}

func (s *Server) Start() error {
        log.Printf("[api] listening on %s", s.http.Addr)
        return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
        return s.http.Shutdown(ctx)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, 200, map[string]string{"status": "ok", "service": "core", "version": "3.8.1"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
        ready := s.readyChecker()
        status := "not_ready"
        if ready {
                status = "ready"
        }
        writeJSON(w, 200, map[string]any{"status": status, "ready": ready})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain; version=0.0.4")
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
        writeJSON(w, 200, map[string]int{"active": active, "resolved_24h": resolved, "failed": failed, "total": len(incidents)})
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

        // v3.8.4: Try full UUID first. If that fails (e.g. the CLI sent an 8-char
        // truncated ID like "91cce5a5" from `incidents list` output), fall back
        // to prefix matching via FindIncidentByPrefix.
        id, err := uuid.Parse(idStr)
        if err == nil {
                // Full UUID — direct lookup.
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
                return
        }

        // Not a full UUID — try prefix matching. v3.8.4: This makes
        // `sentinelctl incidents get 91cce5a5` work, which is what users
        // naturally type after seeing the 8-char truncated ID in
        // `incidents list` output. FindIncidentByPrefix is now part of the
        // Store interface (was a broken type assertion before — the assertion
        // targeted *feedback.Store but s.store is a *storeAdapter wrapper,
        // so it always failed with ok=false and returned 400).
        incident, err := s.store.FindIncidentByPrefix(r.Context(), idStr)
        if err != nil {
                writeJSON(w, 500, map[string]string{"error": err.Error()})
                return
        }
        if incident == nil {
                writeJSON(w, 404, map[string]string{"error": "no incident matches that ID prefix (need at least 4 chars)"})
                return
        }
        writeJSON(w, 200, incident)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
        if r.Method == "GET" {
                writeJSON(w, 200, s.settings())
                return
        }
        // v3.8.0: POST /settings — save runtime config
        if r.Method == "POST" {
                if s.rtCfgSaver == nil {
                        writeJSON(w, 501, map[string]string{"error": "runtime config saver not configured"})
                        return
                }
                body, err := readBody(r)
                if err != nil {
                        writeJSON(w, 400, map[string]string{"error": "read body failed"})
                        return
                }
                if err := s.rtCfgSaver.SaveFromJSON([]byte(body)); err != nil {
                        writeJSON(w, 500, map[string]string{"error": err.Error()})
                        return
                }
                log.Printf("[api] runtime config saved via POST /settings")
                writeJSON(w, 200, map[string]string{"status": "saved", "message": "Configuration saved. Some changes may require a restart to take full effect."})
                return
        }
        writeJSON(w, 405, map[string]string{"error": "method not allowed"})
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
        writeJSON(w, 200, map[string]string{"status": "triggered", "service": spec.Service, "cluster_id": clusterID})
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
        writeJSON(w, 200, map[string]any{"status": "paused", "duration_min": req.DurationMin, "reason": req.Reason})
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

        ch := make(chan ServerEvent, 16)
        s.eventSubsMu.Lock()
        s.eventSubs[ch] = true
        s.eventSubsMu.Unlock()
        defer func() {
                s.eventSubsMu.Lock()
                delete(s.eventSubs, ch)
                s.eventSubsMu.Unlock()
        }()

        fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"connected\"}\n\n")
        flusher.Flush()

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

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
        if s.webhookHandler == nil {
                writeJSON(w, 503, map[string]string{"error": "webhook handler not configured"})
                return
        }
        s.webhookHandler.ServeHTTP(w, r)
}

func (s *Server) handleSlack(w http.ResponseWriter, r *http.Request) {
        if s.slackHandler == nil {
                writeJSON(w, 503, map[string]string{"error": "slack handler not configured"})
                return
        }
        s.slackHandler.ServeHTTP(w, r)
}

func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request) {
        if s.webUIHandler == nil {
                w.Header().Set("Content-Type", "text/html; charset=utf-8")
                w.Write([]byte(`<!DOCTYPE html><html><head><title>Sentinel</title></head><body style="background:#0A0A0B;color:#A1A1AA;font-family:monospace;padding:40px"><h1 style="color:#06B6D4">Sentinel</h1><p>Web UI not built. API at /healthz, /incidents, /metrics.</p></body></html>`))
                return
        }
        s.webUIHandler.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        _ = json.NewEncoder(w).Encode(v)
}

func readBody(r *http.Request) (string, error) {
        data, err := io.ReadAll(r.Body)
        return string(data), err
}
