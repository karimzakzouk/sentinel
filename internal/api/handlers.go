// Package api — HTTP handlers for the Sentinel API server.
//
// All handle* methods on Server. Routes are registered in server.go's
// Start() method.
package api

import (
        "bytes"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "net/http"
        "strconv"
        "strings"
        "time"

        "github.com/google/uuid"

        "github.com/karimzakzouk/sentinel/internal/metrics"
        "github.com/karimzakzouk/sentinel/internal/version"
)


func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, 200, map[string]string{"status": "ok", "service": "core", "version": version.Version})
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
        r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
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
                r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
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

// maxSSESubscribers caps the number of concurrent SSE connections to prevent
// memory exhaustion DoS. v3.13.0: audit finding #10 — SSE is unauthenticated
// and had no cap. Each subscriber pins a goroutine + 16-entry buffered channel
// + map entry (~10KB). 10K connections = ~100MB = OOM on the 256Mi pod.
// 100 subscribers is generous for a single-pod dashboard (one operator = 1-3
// browser tabs = 3-9 connections).
const maxSSESubscribers = 100

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

        s.eventSubsMu.Lock()
        if len(s.eventSubs) >= maxSSESubscribers {
                s.eventSubsMu.Unlock()
                http.Error(w, "too many SSE subscribers (max "+strconv.Itoa(maxSSESubscribers)+")", http.StatusServiceUnavailable)
                return
        }
        ch := make(chan ServerEvent, 16)
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

        // (issue_comment events). Each handler ignores event types it doesn't
        // care about. We buffer the body so both handlers can read it.
        if s.webhookHandler == nil && s.chatopsHandler == nil {
                writeJSON(w, 503, map[string]string{"error": "webhook handler not configured"})
                return
        }

        // Read the body once, dispatch to both handlers via a fresh reader.
        bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB cap
        if err != nil {
                writeJSON(w, 400, map[string]string{"error": "read body failed"})
                return
        }

        // Wrap the body for each handler so they can re-read it.
        multiReader := func() io.Reader { return bytes.NewReader(bodyBytes) }

        // 1. Dispatch to prreview (predictive PR review — handles pull_request events).
        if s.webhookHandler != nil {
                req := r.Clone(r.Context())
                req.Body = io.NopCloser(multiReader())
                s.webhookHandler.ServeHTTP(w, req)
        }

        // 2. Dispatch to chatops (handles issue_comment events for /sentinel commands).
        // Only run if chatops is wired AND the event is issue_comment (the chatops
        // handler checks this internally, but we short-circuit here to avoid
        // buffering + parsing for non-issue_comment events).
        if s.chatopsHandler != nil {
                eventType := r.Header.Get("X-GitHub-Event")
                if eventType == "issue_comment" {
                        req := r.Clone(r.Context())
                        req.Body = io.NopCloser(multiReader())
                        s.chatopsHandler.ServeHTTP(w, req)
                }
        }
}

func (s *Server) handleSlack(w http.ResponseWriter, r *http.Request) {
        if s.slackHandler == nil {
                writeJSON(w, 503, map[string]string{"error": "slack handler not configured"})
                return
        }
        s.slackHandler.ServeHTTP(w, r)
}

// handleGitHubApp manages the GitHub App config. v4.1.0.
//
// GET  /settings/github-app        → status (configured? app_id? slug? install_id?)
// POST /settings/github-app        → store App ID + PEM + optional install ID + webhook secret
// DELETE /settings/github-app      → remove App config (fall back to PAT)
//
// Used by `sentinelctl app setup` + `sentinelctl app status` + `sentinelctl app remove`.
func (s *Server) handleGitHubApp(w http.ResponseWriter, r *http.Request) {
        if s.githubAppManager == nil {
                writeJSON(w, 503, map[string]string{"error": "github app manager not configured"})
                return
        }

        switch r.Method {
        case http.MethodGet:
                status := s.githubAppManager.GetGitHubAppStatus()
                writeJSON(w, 200, status)

        case http.MethodPost:
                r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
                var req struct {
                        AppID         string `json:"app_id"`
                        PrivateKey    string `json:"private_key"`
                        InstallID     string `json:"install_id"`
                        WebhookSecret string `json:"webhook_secret"`
                }
                if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
                        writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
                        return
                }
                if req.AppID == "" || req.PrivateKey == "" {
                        writeJSON(w, 400, map[string]string{"error": "app_id and private_key are required"})
                        return
                }
                slug, err := s.githubAppManager.SetGitHubApp(req.AppID, req.PrivateKey, req.InstallID, req.WebhookSecret)
                if err != nil {
                        writeJSON(w, 500, map[string]string{"error": err.Error()})
                        return
                }
                log.Printf("[api] GitHub App configured via /settings/github-app (app_id=%s, slug=%s)", req.AppID, slug)
                writeJSON(w, 200, map[string]string{
                        "status": "configured",
                        "slug":   slug,
                        "bot":    slug + "[bot]",
                        "hint":   "If install_id was empty, run `sentinelctl app set-install <id>` after installing the App.",
                })

        case http.MethodDelete:
                if err := s.githubAppManager.RemoveGitHubApp(); err != nil {
                        writeJSON(w, 500, map[string]string{"error": err.Error()})
                        return
                }
                log.Printf("[api] GitHub App config removed")
                writeJSON(w, 200, map[string]string{"status": "removed", "message": "Falling back to PAT."})

        default:
                writeJSON(w, 405, map[string]string{"error": "method not allowed"})
        }
}

// handleGitHubAppInstall sets just the installation ID. v4.1.0.
// Called separately from the App credentials because the install ID isn't
// known until the user installs the App on their repo (which happens after
// the App credentials are stored).
func (s *Server) handleGitHubAppInstall(w http.ResponseWriter, r *http.Request) {
        if s.githubAppManager == nil {
                writeJSON(w, 503, map[string]string{"error": "github app manager not configured"})
                return
        }
        if r.Method != http.MethodPost {
                writeJSON(w, 405, map[string]string{"error": "method not allowed"})
                return
        }
        var req struct {
                InstallID string `json:"install_id"`
        }
        if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)).Decode(&req); err != nil {
                writeJSON(w, 400, map[string]string{"error": "invalid JSON: " + err.Error()})
                return
        }
        if req.InstallID == "" {
                writeJSON(w, 400, map[string]string{"error": "install_id is required"})
                return
        }
        if err := s.githubAppManager.SetGitHubAppInstall(req.InstallID); err != nil {
                writeJSON(w, 500, map[string]string{"error": err.Error()})
                return
        }
        log.Printf("[api] GitHub App install ID set: %s", req.InstallID)
        writeJSON(w, 200, map[string]string{"status": "configured", "install_id": req.InstallID})
}

// handleProviders returns the LLM provider pool status. v4.0.0.
// Shows each provider's health: tripped state, last failure, call counts.
// Used by the web UI's "LLM Providers" panel.
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
        if s.providersStatus == nil {
                writeJSON(w, 200, map[string]any{
                        "enabled":  false,
                        "providers": []any{},
                        "message":  "multi-provider pool not configured (single-provider mode)",
                })
                return
        }
        statuses := s.providersStatus.ProviderStatuses()
        writeJSON(w, 200, map[string]any{
                "enabled":   len(statuses) > 0,
                "providers": statuses,
        })
}

// handleAutoMerge returns the auto-merge policy status. v4.0.0.
// Shows: enabled, namespaces, rate limit usage, cooldown state, tracked PRs.
func (s *Server) handleAutoMerge(w http.ResponseWriter, r *http.Request) {
        if s.autoMergeStatus == nil {
                writeJSON(w, 200, map[string]any{
                        "enabled":  false,
                        "message":  "auto-merge not configured",
                })
                return
        }
        writeJSON(w, 200, s.autoMergeStatus.AutoMergeStatus())
}

func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request) {
        if s.webUIHandler == nil {
                w.Header().Set("Content-Type", "text/html; charset=utf-8")
                w.Write([]byte(`<!DOCTYPE html><html><head><title>Sentinel</title></head><body style="background:#0A0A0B;color:#A1A1AA;font-family:monospace;padding:40px"><h1 style="color:#06B6D4">Sentinel</h1><p>Web UI not built. API at /healthz, /incidents, /metrics.</p></body></html>`))
                return
        }
        s.webUIHandler.ServeHTTP(w, r)
}
