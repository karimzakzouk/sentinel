package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// ServerEvent is pushed to SSE clients. Type identifies the event kind
// (e.g. "incident.created", "hypothesis.formed"); Data is the JSON-marshaled
// payload.
type ServerEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// RuntimeCfgSaver saves runtime config. Implemented by runtimecfg.Manager.
type RuntimeCfgSaver interface {
	SaveFromJSON(jsonBytes []byte) error
}

// Store is the incident-storage interface the API needs. Implemented by
// feedback.Store.
type Store interface {
	ListRecentIncidents(ctx context.Context, limit int) ([]*schemas.Incident, error)
	GetIncident(ctx context.Context, id uuid.UUID) (*schemas.Incident, error)
	// FindIncidentByPrefix resolves a short prefix (≥4 chars) to a single
	// incident. Returns (nil, nil) if no match. Used so the CLI's 8-char
	// truncated IDs resolve to the full UUID.
	FindIncidentByPrefix(ctx context.Context, prefix string) (*schemas.Incident, error)
}

// SettingsGetter returns the current runtime settings as a JSON-serializable
// map. Implemented as a function so main.go can wire in any settings source.
type SettingsGetter func() map[string]any

// InvestigateFunc triggers a manual investigation. Returns the cluster ID
// the new investigation is filed under.
type InvestigateFunc func(service, namespace, reason string) (string, error)

// InvestigateWithLogsFunc triggers an investigation with pre-supplied pod logs
// and repo. Used for manual triggers and testing without a live K8s cluster.
type InvestigateWithLogsFunc func(service, namespace, reason, repo, podLogs string) (string, error)

// ReadyChecker reports whether Sentinel is ready to serve traffic.
type ReadyChecker func() bool

// PauseController is the autonomous-mode pause/resume surface. Implemented
// by slackbot.Bot and the chatops Deps.PauseController shim.
type PauseController interface {
	IsPaused() bool
	Pause(duration time.Duration, reason string)
	Resume()
}

// GitHubAppManager is implemented by runtimecfg.Manager. Used by
// /settings/github-app to store + apply App credentials live (no restart).
type GitHubAppManager interface {
	// SetGitHubApp stores the App ID + private key + optional install ID +
	// webhook secret in runtime config, then applies them to all live GitHub
	// clients. Returns the new App slug.
	SetGitHubApp(appID, privateKey, installID, webhookSecret string) (slug string, err error)
	// SetGitHubAppInstall sets just the installation ID (called separately
	// after the user installs the App on their repo).
	SetGitHubAppInstall(installID string) error
	// GetGitHubAppStatus returns the current App config (configured, app_id,
	// slug, install_id).
	GetGitHubAppStatus() schemas.GitHubAppStatus
	// RemoveGitHubApp clears the App config (falls back to PAT).
	RemoveGitHubApp() error
}

// ProvidersStatusGetter returns the LLM provider pool status. Implemented
// by a wrapper around *llm.Pool in main.go.
type ProvidersStatusGetter interface {
	ProviderStatuses() []any
}

// AutoMergeStatusGetter returns the auto-merge policy status + tracked PRs.
// Implemented by a wrapper around *automerge.Policy + *automerge.Merger.
type AutoMergeStatusGetter interface {
	AutoMergeStatus() any
}

// Server is the HTTP API server.
type Server struct {
	store               Store
	settings            SettingsGetter
	investigate         InvestigateFunc
	investigateWithLogs InvestigateWithLogsFunc
	readyChecker        ReadyChecker

	// Sub-handlers wired by main.go. Each is optional; the corresponding
	// route returns 503 when the handler is nil.
	webhookHandler      http.Handler // prreview (pull_request events).
	chatopsHandler      http.Handler // chatops (issue_comment events).
	slackHandler        http.Handler // slackbot.
	alertmanagerHandler http.Handler // alertmanager.Receiver.
	pauseController     PauseController
	webUIHandler        http.Handler
	http                *http.Server
	providersStatus     ProvidersStatusGetter
	autoMergeStatus     AutoMergeStatusGetter
	githubAppManager    GitHubAppManager
	rtCfgSaver          RuntimeCfgSaver

	// SSE subscribers. See handleSSE.
	eventSubs   map[chan ServerEvent]bool
	eventSubsMu sync.Mutex

	// apiToken gates admin endpoints. Empty = unauthenticated (with a
	// startup warning logged).
	apiToken string

	// rateLimiter caps admin + webhook endpoints per IP.
	rateLimiter *ipRateLimiter
}

// ipRateLimiter is a per-IP token bucket. 10 requests per minute per IP —
// generous enough for a human clicking buttons, blocks scripted
// brute-force token attacks.
type ipRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitorBucket
	rate     int
	window   time.Duration
}

type visitorBucket struct {
	count   int
	resetAt time.Time
}

// newIPRateLimiter starts a rate limiter that allows rate requests per
// window per IP. A background goroutine purges stale buckets every 5
// minutes so the map doesn't grow unbounded.
func newIPRateLimiter(rate int, window time.Duration) *ipRateLimiter {
	rl := &ipRateLimiter{
		visitors: make(map[string]*visitorBucket),
		rate:     rate,
		window:   window,
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Info("api: rate-limiter cleanup panic recovered", "r", r)
			}
		}()
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

// Allow reports whether addr is under the rate limit. Extracts the IP from
// addr (strips port; handles IPv6 brackets).
func (rl *ipRateLimiter) Allow(addr string) bool {
	ip := addr
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		ip = addr[:idx]
	}
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

// New creates a new API server bound to addr. Wire optional handlers via
// the Set* methods before calling Start.
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
	mux.HandleFunc("/admin/whoami", s.handleWhoami)
	mux.HandleFunc("/admin/pause", s.handlePause)
	mux.HandleFunc("/admin/resume", s.handleResume)
	mux.HandleFunc("/events", s.handleSSE)
	mux.HandleFunc("/webhook/github", s.handleWebhook)
	mux.HandleFunc("/webhook/alertmanager", s.handleAlertmanager)
	mux.HandleFunc("/slack/events", s.handleSlack)
	mux.HandleFunc("/providers", s.handleProviders)
	mux.HandleFunc("/automerge", s.handleAutoMerge)
	mux.HandleFunc("/api/meta", s.handleAPIMeta)
	mux.HandleFunc("/", s.handleWebUI)

	s.http = &http.Server{
		Addr:              addr,
		Handler:           s.authMiddleware(mux),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		// WriteTimeout is intentionally 0 (infinite) so SSE /events can
		// stream indefinitely. Non-SSE routes are wrapped in
		// http.TimeoutHandler where a hard cap is appropriate.
	}
	return s
}

// SetAPIToken enables bearer-token auth on admin endpoints. Empty = disabled
// (with a startup warning). When set, every request to /admin/* or
// POST /settings must carry 'Authorization: Bearer <token>'. Mismatched or
// missing tokens get 401. Health/readiness/metrics and the web-UI shell
// remain unauthenticated so probes and dashboards still work. The SSE
// /events stream is token-gated too — via the ?token= query param, because
// EventSource cannot set Authorization headers (the web UI appends it
// automatically).
func (s *Server) SetAPIToken(token string) {
	s.apiToken = token
	if token == "" {
		logging.Warn("SENTINEL_API_TOKEN is empty — admin API is unauthenticated. Set it in production to prevent unauthorized investigation triggers, pause/resume, and settings changes.")
	} else {
		logging.Info("api: admin auth enabled (bearer token set)")
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
//   - /debug/pprof/*      (profiling endpoints)
//
// Webhook and Slack endpoints do their own signature verification and are
// not gated by this middleware (but are rate-limited).
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Rate-limit admin + webhook endpoints to prevent brute-force
		// token attacks and webhook flood DoS. Health/metrics/incidents
		// reads are not rate-limited (kubelet + Prometheus scrape
		// frequently).
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
			if strings.HasPrefix(r.URL.Path, "/debug/pprof") {
				writeJSON(w, 403, map[string]string{"error": "pprof requires SENTINEL_API_TOKEN to be set"})
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Public routes: healthz/readyz/metrics (kubelet + Prometheus),
		// the web UI itself (/ and /index.html), and static assets
		// (/_next/* — JS/CSS/images). These contain no data — just the
		// HTML/JS shell. Data is fetched via authenticated API calls
		// from the browser.
		isPublic := r.URL.Path == "/healthz" ||
			r.URL.Path == "/readyz" ||
			r.URL.Path == "/metrics" ||
			r.URL.Path == "/metrics-summary" ||
			r.URL.Path == "/api/meta" ||
			r.URL.Path == "/" ||
			r.URL.Path == "/index.html" ||
			strings.HasPrefix(r.URL.Path, "/_next/")

		if isPublic {
			next.ServeHTTP(w, r)
			return
		}

		// SSE (EventSource) can't set Authorization headers, so accept
		// the token via ?token= query param for /events only.
		if r.URL.Path == "/events" && s.apiToken != "" {
			token := r.URL.Query().Get("token")
			if token != "" && secureCompare(token, s.apiToken) {
				next.ServeHTTP(w, r)
				return
			}
		}

		// GET /settings is read-only — allow without a token so the
		// dashboard can render the settings panel. POST /settings still
		// requires a token (handled below).
		if r.URL.Path == "/settings" && r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		// GET /providers, /automerge, /incidents, /incidents/{id} are
		// read-only dashboard data — allow without a token.
		if r.Method == http.MethodGet && (r.URL.Path == "/providers" || r.URL.Path == "/automerge" ||
			r.URL.Path == "/incidents" || strings.HasPrefix(r.URL.Path, "/incidents/")) {
			next.ServeHTTP(w, r)
			return
		}

		// Require a bearer token for everything else.
		authHeader := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			writeJSON(w, 401, map[string]string{
				"error": "missing or malformed Authorization header (expected 'Bearer <token>')",
			})
			return
		}
		provided := authHeader[len(prefix):]
		if !secureCompare(provided, s.apiToken) {
			writeJSON(w, 401, map[string]string{"error": "invalid token"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// secureCompare does a constant-time string comparison so an attacker
// cannot time-side-channel the token byte by byte. Wraps
// crypto/subtle.ConstantTimeCompare; returns false on length mismatch
// (a length leak, but the length is already obvious from the header).
func secureCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// SetWebhookHandler wires the prreview webhook handler (pull_request events).
func (s *Server) SetWebhookHandler(h http.Handler) { s.webhookHandler = h }

// SetChatopsHandler wires the chatops webhook handler (issue_comment events).
func (s *Server) SetChatopsHandler(h http.Handler) { s.chatopsHandler = h }

// SetSlackHandler wires the Slack Events API handler.
func (s *Server) SetSlackHandler(h http.Handler) { s.slackHandler = h }

// SetAlertmanagerHandler wires the Alertmanager webhook receiver.
func (s *Server) SetAlertmanagerHandler(h http.Handler) { s.alertmanagerHandler = h }

// SetPauseController wires the autonomous-mode pause/resume controller.
func (s *Server) SetPauseController(pc PauseController) { s.pauseController = pc }

// SetWebUIHandler wires the embedded Next.js dashboard handler.
func (s *Server) SetWebUIHandler(h http.Handler) { s.webUIHandler = h }

// SetRuntimeCfgSaver wires the runtime-config saver (runtimecfg.Manager).
func (s *Server) SetRuntimeCfgSaver(r RuntimeCfgSaver) { s.rtCfgSaver = r }

// SetGitHubAppManager wires the GitHub App manager (runtimecfg.Manager).
func (s *Server) SetGitHubAppManager(m GitHubAppManager) { s.githubAppManager = m }

// SetProvidersStatusGetter wires the LLM provider pool status getter.
func (s *Server) SetProvidersStatusGetter(g ProvidersStatusGetter) { s.providersStatus = g }

func (s *Server) SetInvestigateWithLogs(f InvestigateWithLogsFunc) { s.investigateWithLogs = f }

// SetAutoMergeStatusGetter wires the auto-merge policy status getter.
func (s *Server) SetAutoMergeStatusGetter(g AutoMergeStatusGetter) { s.autoMergeStatus = g }

// BroadcastEvent pushes an event to all connected SSE clients. Safe for
// concurrent use. Slow subscribers (full channel) are dropped via the
// non-blocking select so a stalled client cannot block the broadcaster.
// Implements events.Broadcaster.
func (s *Server) BroadcastEvent(eventType string, data any) {
	s.eventSubsMu.Lock()
	defer s.eventSubsMu.Unlock()
	for ch := range s.eventSubs {
		select {
		case ch <- ServerEvent{Type: eventType, Data: data}:
		default:
		}
	}
}

// Start binds the listener and blocks until the server stops. Returns the
// error from ListenAndServe (typically http.ErrServerClosed after Shutdown).
func (s *Server) Start() error {
	logging.Info("api: listening", "addr", s.http.Addr)
	return s.http.ListenAndServe()
}

// Shutdown gracefully stops the server, waiting for in-flight requests to
// complete or ctx to expire.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
