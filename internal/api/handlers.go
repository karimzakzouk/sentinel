package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/internal/chatops"
	"github.com/karimzakzouk/sentinel/internal/logging"
	"github.com/karimzakzouk/sentinel/internal/metrics"
	"github.com/karimzakzouk/sentinel/internal/schemas"
	"github.com/karimzakzouk/sentinel/internal/version"
)

// handleHealthz is the liveness probe — always 200 with the version + auth state.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"status":        "ok",
		"service":       "core",
		"version":       version.Version,
		"auth_required": s.apiToken != "",
	})
}

// handleReadyz is the readiness probe — 200 only when readyChecker returns true.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ready := s.readyChecker()
	status := "not_ready"
	if ready {
		status = "ready"
	}
	writeJSON(w, 200, map[string]any{"status": status, "ready": ready})
}

// handleMetrics serves the Prometheus exposition format from the metrics package.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	metrics.Handler().ServeHTTP(w, r)
}

// handleMetricsSummary returns a count of incidents by lifecycle bucket
// (active / resolved / failed) over the most recent 100 incidents.
func (s *Server) handleMetricsSummary(w http.ResponseWriter, r *http.Request) {
	incidents, _ := s.store.ListRecentIncidents(r.Context(), 100)
	active, resolved, failed := 0, 0, 0
	for _, inc := range incidents {
		status := string(inc.Status)
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

// handleIncidents lists recent incidents (GET) with a limit query param.
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
	// Mirror pr_number/pr_url/etc from state.execution_result → top-level
	// fields so the frontend's PRs tab + incident detail can read them
	// without digging into the nested state object.
	for _, inc := range incidents {
		enrichIncidentPRFields(inc)
	}
	writeJSON(w, 200, incidents)
}

// handleIncident returns a single incident by full UUID or short prefix.
func (s *Server) handleIncident(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/incidents/")

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
		enrichIncidentPRFields(incident)
		writeJSON(w, 200, incident)
		return
	}

	// Not a full UUID — try prefix matching. FindIncidentByPrefix requires
	// ≥4 chars to prevent accidental collisions on short prefixes.
	incident, err := s.store.FindIncidentByPrefix(r.Context(), idStr)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	if incident == nil {
		writeJSON(w, 404, map[string]string{"error": "no incident matches that ID prefix (need at least 4 chars)"})
		return
	}
	enrichIncidentPRFields(incident)
	writeJSON(w, 200, incident)
}

// enrichIncidentPRFields mirrors pr_number/pr_url/pr_branch +
// sandbox_summary + sandbox_verified from the nested state.execution_result
// (which is what the agents write to) onto the top-level Incident fields
// (which is what the frontend reads). Keeps the JSON contract stable
// without rewriting every SELECT.
func enrichIncidentPRFields(inc *schemas.Incident) {
	if inc == nil {
		return
	}
	if er := inc.State.ExecutionResult; er != nil {
		if inc.PRNumber == nil && er.PRNumber != nil {
			inc.PRNumber = er.PRNumber
		}
		if inc.PRURL == "" && er.PRURL != nil {
			inc.PRURL = *er.PRURL
		}
		if inc.PRBranch == "" && er.PRBranch != nil {
			inc.PRBranch = *er.PRBranch
		}
		if inc.SandboxSummary == "" && er.SandboxSummary != nil {
			inc.SandboxSummary = *er.SandboxSummary
		}
		if !inc.SandboxVerified && er.SandboxVerified {
			inc.SandboxVerified = er.SandboxVerified
		}
	}
	if fp := inc.State.FixProposal; fp != nil {
		if inc.FixType == "" {
			inc.FixType = string(fp.FixType)
		}
	}
}

// handleSettings GET returns the current settings; POST saves new settings
// via the runtimecfg saver.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, 200, s.settings())
		return
	}

	if r.Method == http.MethodPost {
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
		logging.Info("api: runtime config saved via POST /settings")
		writeJSON(w, 200, map[string]string{
			"status":  "saved",
			"message": "Configuration saved. Some changes may require a restart to take full effect.",
		})
		return
	}
	writeJSON(w, 405, map[string]string{"error": "method not allowed"})
}

// handleWhoami returns 200 if the caller has a valid token. Used by the
// frontend LoginGate to validate the token against an authenticated endpoint.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleInvestigate is the manual-trigger endpoint. POST with a JSON body
// {service, namespace, reason} triggers a new investigation.
func (s *Server) handleInvestigate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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
	writeJSON(w, 200, map[string]string{
		"status":     "triggered",
		"service":    spec.Service,
		"cluster_id": clusterID,
	})
}

// handlePause pauses autonomous mode for a configurable duration (default 30min).
func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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
	writeJSON(w, 200, map[string]any{
		"status":       "paused",
		"duration_min": req.DurationMin,
		"reason":       req.Reason,
	})
}

// handleResume resumes autonomous mode immediately.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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

// maxSSESubscribers caps concurrent SSE connections to prevent memory
// exhaustion DoS. SSE is unauthenticated (when apiToken is empty) and
// each subscriber pins a goroutine + 16-entry buffered channel + map
// entry (~10KB). 100 subscribers is generous for a single-pod dashboard.
const maxSSESubscribers = 100

// handleSSE streams events to the client. Subscribes to the broadcaster,
// writes "data: {...}\n\n" frames, and flushes on each event. A 15s
// keep-alive ping prevents intermediate proxies from closing the connection.
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

	// Send events as unnamed SSE messages (no `event:` line) so the
	// frontend's EventSource.onmessage handler fires for every event.
	// The event type is embedded in the JSON payload's `type` field.
	fmt.Fprintf(w, "data: {\"type\":\"connected\",\"status\":\"connected\"}\n\n")
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-ch:
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// handleWebhook dispatches /webhook/github to the prreview + chatops
// handlers. The body is buffered once so both handlers can read it; each
// handler ignores event types it doesn't care about.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhookHandler == nil && s.chatopsHandler == nil {
		writeJSON(w, 503, map[string]string{"error": "webhook handler not configured"})
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB cap.
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "read body failed"})
		return
	}
	multiReader := func() io.Reader { return bytes.NewReader(bodyBytes) }

	// 1. Dispatch to prreview (predictive PR review — pull_request events).
	if s.webhookHandler != nil {
		req := r.Clone(r.Context())
		req.Body = io.NopCloser(multiReader())
		s.webhookHandler.ServeHTTP(w, req)
	}

	// 2. Dispatch to chatops (issue_comment events for /sentinel commands).
	// Short-circuit for non-issue_comment events so we don't buffer +
	// parse for events chatops doesn't care about.
	if s.chatopsHandler != nil {
		eventType := r.Header.Get("X-GitHub-Event")
		if eventType == "issue_comment" {
			req := r.Clone(r.Context())
			req.Body = io.NopCloser(multiReader())
			s.chatopsHandler.ServeHTTP(w, req)
		}
	}
}

// handleSlack forwards /slack/events to the Slack Events API handler.
func (s *Server) handleSlack(w http.ResponseWriter, r *http.Request) {
	if s.slackHandler == nil {
		writeJSON(w, 503, map[string]string{"error": "slack handler not configured"})
		return
	}
	s.slackHandler.ServeHTTP(w, r)
}

// handleAlertmanager forwards /webhook/alertmanager to the Alertmanager
// webhook receiver.
func (s *Server) handleAlertmanager(w http.ResponseWriter, r *http.Request) {
	if s.alertmanagerHandler == nil {
		writeJSON(w, 503, map[string]string{"error": "alertmanager handler not configured"})
		return
	}
	s.alertmanagerHandler.ServeHTTP(w, r)
}

// handleGitHubApp manages the GitHub App config.
// GET    → status (configured? app_id? slug? install_id?).
// POST   → store App ID + PEM + optional install ID + webhook secret.
// DELETE → remove App config (fall back to PAT).
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
		logging.Info("api: GitHub App configured", "app_id", req.AppID, "slug", slug)
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
		logging.Info("api: GitHub App config removed")
		writeJSON(w, 200, map[string]string{
			"status":  "removed",
			"message": "Falling back to PAT.",
		})

	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}

// handleGitHubAppInstall sets just the installation ID. Called separately
// from the App credentials because the install ID isn't known until the
// user installs the App on their repo.
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
	logging.Info("api: GitHub App install ID set", "install_id", req.InstallID)
	writeJSON(w, 200, map[string]string{"status": "configured", "install_id": req.InstallID})
}

// handleProviders returns the LLM provider pool status. Used by the web UI's
// "LLM Providers" panel.
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	if s.providersStatus == nil {
		writeJSON(w, 200, map[string]any{
			"enabled":   false,
			"providers": []any{},
			"message":   "multi-provider pool not configured (single-provider mode)",
		})
		return
	}
	statuses := s.providersStatus.ProviderStatuses()
	writeJSON(w, 200, map[string]any{
		"enabled":   len(statuses) > 0,
		"providers": statuses,
	})
}

// handleAutoMerge returns the auto-merge policy status + tracked PRs.
func (s *Server) handleAutoMerge(w http.ResponseWriter, r *http.Request) {
	if s.autoMergeStatus == nil {
		writeJSON(w, 200, map[string]any{
			"enabled": false,
			"message": "auto-merge not configured",
		})
		return
	}
	writeJSON(w, 200, s.autoMergeStatus.AutoMergeStatus())
}

// handleAPIMeta returns the chatops command list + available API endpoints
// so the frontend doesn't have to hardcode them. This avoids the v5 bug
// where the frontend drifted from the backend command registry.
func (s *Server) handleAPIMeta(w http.ResponseWriter, r *http.Request) {
	// chatopsCommands is the serialized form of chatops.AllCommands(). Built
	// once per request from the live registry — never hardcoded.
	type chatopsCommandMeta struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Usage       string `json:"usage"`
		Example     string `json:"example"`
		Permission  string `json:"permission"`
		Tier        string `json:"tier"`
		Cancelable  bool   `json:"cancelable"`
	}
	commands := make([]chatopsCommandMeta, 0, len(chatops.AllCommands()))
	for _, c := range chatops.AllCommands() {
		commands = append(commands, chatopsCommandMeta{
			Name:        c.Name,
			Description: c.Description,
			Usage:       c.Usage,
			Example:     c.Example,
			Permission:  c.Permission.String(),
			Tier:        c.Tier,
			Cancelable:  c.Cancelable,
		})
	}
	writeJSON(w, 200, map[string]any{
		"version":       version.Version,
		"endpoints":     apiMetaEndpoints,
		"commands":      commands,
		"auth_required": s.apiToken != "",
	})
}

// apiMetaEndpoints is the static list of routes the API exposes. Kept as a
// package-level var so it isn't reallocated on every /api/meta call.
var apiMetaEndpoints = []string{
	"/healthz",
	"/readyz",
	"/incidents",
	"/incidents/{id}",
	"/admin/investigate",
	"/admin/pause",
	"/admin/resume",
	"/settings",
	"/settings/github-app",
	"/settings/github-app-install",
	"/providers",
	"/automerge",
	"/metrics",
	"/metrics-summary",
	"/events",
	"/api/meta",
}

// handleWebUI serves the embedded Next.js dashboard. Falls back to a small
// placeholder HTML page when the UI is not built.
func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request) {
	if s.webUIHandler == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!DOCTYPE html><html><head><title>Sentinel</title></head><body style="background:#0A0A0B;color:#A1A1AA;font-family:monospace;padding:40px"><h1 style="color:#06B6D4">Sentinel</h1><p>Web UI not built. API at /healthz, /incidents, /metrics.</p></body></html>`))
		return
	}
	s.webUIHandler.ServeHTTP(w, r)
}

// handlePprof delegates to net/http/pprof for production profiling. The
// URL path selects the pprof function — pprof.Index handles the index
// page + heap/goroutine/threadcreate/block/mutex profiles.
func (s *Server) handlePprof(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/debug/pprof/cmdline":
		pprof.Cmdline(w, r)
	case "/debug/pprof/profile":
		pprof.Profile(w, r)
	case "/debug/pprof/symbol":
		pprof.Symbol(w, r)
	case "/debug/pprof/trace":
		pprof.Trace(w, r)
	default:
		pprof.Index(w, r)
	}
}
