// Package api provides the HTTP API server for Sentinel.
package api

import (
        "context"
        "crypto/subtle"
        "log"
        "net/http"
        "strings"
        "sync"
        "time"

        "github.com/google/uuid"
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
        alertmanagerHandler http.Handler // POST /webhook/alertmanager
        pauseController PauseController
        webUIHandler    http.Handler
        http            *http.Server

        // When set, /webhook/github first calls webhookHandler (prreview — handles
        // pull_request events for predictive PR review). If webhookHandler doesn't
        // claim the event (returns 200 without writing), the chatops handler runs
        // (handles issue_comment events for /sentinel commands).

        chatopsHandler http.Handler

        providersStatus ProvidersStatusGetter

        autoMergeStatus AutoMergeStatusGetter

        githubAppManager GitHubAppManager

        eventSubs   map[chan ServerEvent]bool
        eventSubsMu sync.Mutex

        rtCfgSaver RuntimeCfgSaver

        // disabled (the server logs a loud warning at startup). When set,
        // every request to /admin/* or POST /settings must carry
        // 'Authorization: Bearer <token>'. Health/readiness/metrics/SSE/web UI
        // remain unauthenticated so dashboards and probes still work.

        apiToken string

        // Prevents brute-force token attacks and webhook flood DoS.

        rateLimiter *ipRateLimiter
}

// ipRateLimiter is a simple per-IP token bucket rate limiter.
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
                defer func() { if r := recover(); r != nil { log.Printf("[api] rate-limiter cleanup panic recovered: %v", r) } }()
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

        // pprof endpoints for production profiling. Mounted at /debug/pprof/
        // so they're discoverable by Go tooling. Protected by the apiToken
        // auth middleware (if set) — same as /admin/* endpoints.

        mux.HandleFunc("/debug/pprof/", s.handlePprof)
        mux.HandleFunc("/debug/pprof/cmdline", s.handlePprof)
        mux.HandleFunc("/debug/pprof/profile", s.handlePprof)
        mux.HandleFunc("/debug/pprof/symbol", s.handlePprof)
        mux.HandleFunc("/debug/pprof/trace", s.handlePprof)

        mux.HandleFunc("/settings/github-app", s.handleGitHubApp)
        mux.HandleFunc("/settings/github-app-install", s.handleGitHubAppInstall)
        mux.HandleFunc("/admin/investigate", s.handleInvestigate)
        mux.HandleFunc("/admin/pause", s.handlePause)
        mux.HandleFunc("/admin/resume", s.handleResume)
        mux.HandleFunc("/events", s.handleSSE)
        mux.HandleFunc("/webhook/github", s.handleWebhook)
        mux.HandleFunc("/webhook/alertmanager", s.handleAlertmanager)
        mux.HandleFunc("/slack/events", s.handleSlack)

        mux.HandleFunc("/providers", s.handleProviders)

        mux.HandleFunc("/automerge", s.handleAutoMerge)
        mux.HandleFunc("/", s.handleWebUI)

        // on a bearer token. The middleware is a no-op when apiToken is empty
        // (set via SetAPIToken), but we still register it so the wrapping order
        // is consistent.

        s.http = &http.Server{
                Addr:         addr,
                Handler:      s.authMiddleware(mux),
                ReadTimeout:  10 * time.Second,
                ReadHeaderTimeout: 5 * time.Second,
                IdleTimeout:  60 * time.Second,

                // WriteTimeout is intentionally 0 (infinite) so SSE /events can stream
                // indefinitely. Non-SSE routes are wrapped in http.TimeoutHandler where
                // appropriate (admin operations get a 60s hard cap).

        }
        return s
}

// SetAPIToken enables bearer-token auth on admin endpoints. Empty = disabled.
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
// Admin endpoints (require auth when apiToken is set):
//   - /admin/investigate (POST)
//   - /admin/pause        (POST)
//   - /admin/resume       (POST)
//   - /settings           (POST only — GET is read-only)
// Webhook and Slack endpoints do their own signature verification and are
// not gated by this middleware.
// (10 req/min) to prevent brute-force token attacks and webhook flood DoS.

func (s *Server) authMiddleware(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

                // Health/metrics/incidents reads are not rate-limited (kubelet + Prometheus scrape frequently).

                if strings.HasPrefix(r.URL.Path, "/admin/") ||
                        r.URL.Path == "/webhook/github" ||
                        r.URL.Path == "/webhook/alertmanager" ||
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

                isAdmin := strings.HasPrefix(r.URL.Path, "/admin/") ||
				strings.HasPrefix(r.URL.Path, "/debug/pprof/")
                isSettingsWrite := r.URL.Path == "/settings" && r.Method == http.MethodPost

                // repo, branch, PR URL) is sensitive.
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
func (s *Server) SetAlertmanagerHandler(h http.Handler) { s.alertmanagerHandler = h }
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
