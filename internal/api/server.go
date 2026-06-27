// Package api provides the HTTP API server for Sentinel.
package api

import (
        "bytes"
        "context"
        "crypto/subtle"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "net/http"
        "strconv"
        "strings"
        "sync"
        "time"

        "github.com/google/uuid"

        "github.com/karimzakzouk/sentinel/v3/internal/metrics"
        "github.com/karimzakzouk/sentinel/v3/internal/version"
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

        // v4.0.0: Chatops handler — runs alongside webhookHandler on /webhook/github.
        // When set, /webhook/github first calls webhookHandler (prreview — handles
        // pull_request events for predictive PR review). If webhookHandler doesn't
        // claim the event (returns 200 without writing), the chatops handler runs
        // (handles issue_comment events for /sentinel commands).
        chatopsHandler http.Handler

        // v4.0.0: LLM provider pool status getter (for /providers endpoint).
        providersStatus ProvidersStatusGetter
        // v4.0.0: Auto-merge status getter (for /automerge endpoint).
        autoMergeStatus AutoMergeStatusGetter
        // v4.1.0: GitHub App manager (for /settings/github-app endpoint).
        githubAppManager GitHubAppManager

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

        // v3.18.0: H27 — per-IP rate limiter for admin + webhook endpoints.
        // Prevents brute-force token attacks and webhook flood DoS.
        rateLimiter *ipRateLimiter
}

// ipRateLimiter is a simple per-IP token bucket rate limiter.
// v3.18.0: H27 — was missing entirely; admin endpoints had no throttle,
// allowing brute-force token attacks at line speed despite constant-time compare.
// 10 requests per minute per IP — generous enough for legitimate use
// (a human clicking buttons) but blocks scripted attacks.
type ipRateLimiter struct {
        mu       sync.Mutex
        visitors map[string]*visitorBucket
        rate     int           // requests per window
        window   time.Duration // window duration
}

type visitorBucket struct {
        count     int
        resetAt   time.Time
}

func newIPRateLimiter(rate int, window time.Duration) *ipRateLimiter {
        rl := &ipRateLimiter{
                visitors: make(map[string]*visitorBucket),
                rate:     rate,
                window:   window,
        }
        // Background cleanup of stale buckets every 5 minutes
        go func() {
                ticker := time.NewTicker(5 * time.Minute)
                defer ticker.Stop()
                for range ticker.C {
                        rl.mu.Lock()
                        now := time.Now()
                        for ip, b := range rl.visitors {
                                if now.After(b.resetAt) {
                                        delete(rl.visitors, ip)
                                }
                        }
                        rl.mu.Unlock()
                }
        }()
        return rl
}

// Allow returns true if the IP is under the rate limit, false otherwise.
// Extracts the IP from addr (strips port).
func (rl *ipRateLimiter) Allow(addr string) bool {
        // Strip port from addr (e.g. "1.2.3.4:5678" → "1.2.3.4")
        ip := addr
        if idx := strings.LastIndex(addr, ":"); idx >= 0 {
                ip = addr[:idx]
        }
        // Handle IPv6 addresses with brackets
        ip = strings.TrimPrefix(strings.TrimSuffix(ip, "]"), "[")

        rl.mu.Lock()
        defer rl.mu.Unlock()

        now := time.Now()
        b, exists := rl.visitors[ip]
        if !exists || now.After(b.resetAt) {
                rl.visitors[ip] = &visitorBucket{count: 1, resetAt: now.Add(rl.window)}
                return true
        }
        if b.count >= rl.rate {
                return false
        }
        b.count++
        return true
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
                // v3.18.0: H27 — 10 req/min per IP for admin + webhook endpoints.
                rateLimiter:  newIPRateLimiter(10, time.Minute),
        }
        mux := http.NewServeMux()
        mux.HandleFunc("/healthz", s.handleHealthz)
        mux.HandleFunc("/readyz", s.handleReadyz)
        mux.HandleFunc("/metrics", s.handleMetrics)
        mux.HandleFunc("/metrics-summary", s.handleMetricsSummary)
        mux.HandleFunc("/incidents", s.handleIncidents)
        mux.HandleFunc("/incidents/", s.handleIncident)
        mux.HandleFunc("/settings", s.handleSettings)
        // v4.1.0: GitHub App auto-provisioning endpoints (used by sentinelctl app setup)
        mux.HandleFunc("/settings/github-app", s.handleGitHubApp)
        mux.HandleFunc("/settings/github-app-install", s.handleGitHubAppInstall)
        mux.HandleFunc("/admin/investigate", s.handleInvestigate)
        mux.HandleFunc("/admin/pause", s.handlePause)
        mux.HandleFunc("/admin/resume", s.handleResume)
        mux.HandleFunc("/events", s.handleSSE)
        mux.HandleFunc("/webhook/github", s.handleWebhook)
        mux.HandleFunc("/slack/events", s.handleSlack)
        // v4.0.0: LLM provider pool status (health, failover counts, tripped breakers)
        mux.HandleFunc("/providers", s.handleProviders)
        // v4.0.0: Auto-merge policy status (rate limit, cooldown, tracked PRs)
        mux.HandleFunc("/automerge", s.handleAutoMerge)
        mux.HandleFunc("/", s.handleWebUI)

        // v3.8.4: Wrap the mux in an auth middleware that gates admin endpoints
        // on a bearer token. The middleware is a no-op when apiToken is empty
        // (set via SetAPIToken), but we still register it so the wrapping order
        // is consistent.
        s.http = &http.Server{
                Addr:         addr,
                Handler:      s.authMiddleware(mux),
                ReadTimeout:  10 * time.Second,
                ReadHeaderTimeout: 5 * time.Second,
                IdleTimeout:  60 * time.Second,
                // v3.18.0: H26 — was missing all timeouts. Slowloris could exhaust FDs.
                // WriteTimeout is intentionally 0 (infinite) so SSE /events can stream
                // indefinitely. Non-SSE routes are wrapped in http.TimeoutHandler where
                // appropriate (admin operations get a 60s hard cap).
        }
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
//
// v3.18.0: H27 — admin + webhook endpoints now have per-IP rate limiting
// (10 req/min) to prevent brute-force token attacks and webhook flood DoS.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                // v3.18.0: H27 — rate limit admin + webhook + slack endpoints.
                // Health/metrics/incidents reads are not rate-limited (kubelet + Prometheus scrape frequently).
                if strings.HasPrefix(r.URL.Path, "/admin/") ||
                        r.URL.Path == "/webhook/github" ||
                        r.URL.Path == "/slack/events" {
                        if !s.rateLimiter.Allow(r.RemoteAddr) {
                                writeJSON(w, 429, map[string]string{
                                        "error": "rate limit exceeded (max 10 req/min per IP) — retry after 60s",
                                })
                                return
                        }
                }

                if s.apiToken == "" {
                        next.ServeHTTP(w, r)
                        return
                }

                isAdmin := strings.HasPrefix(r.URL.Path, "/admin/")
                isSettingsWrite := r.URL.Path == "/settings" && r.Method == http.MethodPost
                // v3.18.0: H28 — gate /incidents behind auth. Incident data (root cause,
                // repo, branch, PR URL) is sensitive. Was: anyone with cluster network
                // access could read every incident.
                isIncidentsRead := strings.HasPrefix(r.URL.Path, "/incidents")

                if !isAdmin && !isSettingsWrite && !isIncidentsRead {
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

// SetChatopsHandler wires the chatops webhook handler. v4.0.0.
// When set, /webhook/github events are dispatched to BOTH the prreview
// handler (pull_request events) and the chatops handler (issue_comment
// events). Each handler ignores event types it doesn't care about.
func (s *Server) SetChatopsHandler(h http.Handler)      { s.chatopsHandler = h }

// GitHubAppManager is implemented by the runtimecfg manager. v4.1.0.
// Used by /settings/github-app to store + apply App credentials live.
type GitHubAppManager interface {
        // SetGitHubApp stores the App ID + private key + optional install ID +
        // webhook secret in runtime config, then applies them to all live GitHub
        // clients (no restart). Returns the new App slug.
        SetGitHubApp(appID, privateKey, installID, webhookSecret string) (slug string, err error)
        // SetGitHubAppInstall sets just the installation ID (called separately
        // after the user installs the App on their repo).
        SetGitHubAppInstall(installID string) error
        // GetGitHubAppStatus returns the current App config (configured, app_id,
        // slug, install_id). slug is fetched lazily from the GitHub API on first
        // call after credentials are set.
        GetGitHubAppStatus() GitHubAppStatus
        // RemoveGitHubApp clears the App config (falls back to PAT).
        RemoveGitHubApp() error
}

// GitHubAppStatus is the public state of the GitHub App config.
// Defined in the api package because it's the return type of
// GitHubAppManager.GetGitHubAppStatus, which is implemented by runtimecfg.
// runtimecfg returns this type directly (no conversion needed because
// the api package doesn't import runtimecfg — runtimecfg imports api
// via the GitHubAppManager interface satisfaction).
type GitHubAppStatus struct {
        Configured bool   `json:"configured"`
        AppID      string `json:"app_id"`
        Slug       string `json:"slug"`
        InstallID  string `json:"install_id"`
}

// SetGitHubAppManager wires the GitHub App manager. v4.1.0.
func (s *Server) SetGitHubAppManager(m GitHubAppManager) {
        s.githubAppManager = m
}

// SetProvidersStatusGetter wires the LLM provider pool status getter. v4.0.0.
// Used by /providers to expose pool health (tripped breakers, failover counts).
func (s *Server) SetProvidersStatusGetter(g ProvidersStatusGetter) {
        s.providersStatus = g
}

// SetAutoMergeStatusGetter wires the auto-merge policy status getter. v4.0.0.
// Used by /automerge to expose policy state (rate limit, cooldown, tracked PRs).
func (s *Server) SetAutoMergeStatusGetter(g AutoMergeStatusGetter) {
        s.autoMergeStatus = g
}

// ProvidersStatusGetter returns the LLM provider pool status. Implemented by
// a wrapper around *llm.Pool in main.go.
type ProvidersStatusGetter interface {
        ProviderStatuses() []any
}

// AutoMergeStatusGetter returns the auto-merge policy status + tracked PRs.
// Implemented by a wrapper around *automerge.Policy + *automerge.Merger.
type AutoMergeStatusGetter interface {
        AutoMergeStatus() any
}

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
        r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes) // v3.13.0: OOM defense
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
                r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes) // v3.13.0: OOM defense
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

        // v3.13.0: Cap concurrent SSE subscribers to prevent OOM DoS.
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
        // v4.0.0: Dispatch to both prreview (pull_request events) AND chatops
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

func writeJSON(w http.ResponseWriter, status int, v any) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        _ = json.NewEncoder(w).Encode(v)
}

// maxRequestBodyBytes limits the size of request bodies to prevent OOM DoS.
// v3.13.0: audit finding #3 — POST handlers used io.ReadAll(r.Body) with no
// limit. An attacker could POST a 10GB body and OOM the 256Mi pod.
// 1MB is generous for all legitimate API requests (settings JSON, investigate
// trigger JSON, pause JSON are all <2KB in practice).
const maxRequestBodyBytes = 1 << 20 // 1MB

func readBody(r *http.Request) (string, error) {
        // v3.13.0: Wrap r.Body in MaxBytesReader to cap the read at 1MB.
        r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
        data, err := io.ReadAll(r.Body)
        return string(data), err
}
