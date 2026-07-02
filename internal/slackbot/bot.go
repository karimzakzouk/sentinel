// Package slackbot implements an interactive two-way Slack bot for Sentinel.
// The bot reports what it's doing as it works (investigation started, fix
// proposed, PR opened) and accepts commands via @sentinel mentions or slash
// commands:
//   - "pause autonomous mode for X"  — pauses auto-investigation for X minutes
//   - "investigate Y"                — manually triggers investigation for service Y
//   - "show me the last 5 incidents" — lists recent incidents
//   - "status"                       — shows current autonomous mode status
//   - "resume"                       — resumes autonomous mode
//
// Uses the Slack Events API (not RTM — simpler, webhook-based).
// Endpoints added to api/server.go:
//
//      POST /slack/events — receives Slack Events API + slash command payloads
package slackbot

import (
        "bytes"
        "context"
        "crypto/hmac"
        "crypto/sha256"
        "encoding/hex"
        "encoding/json"
        "fmt"
        "github.com/karimzakzouk/sentinel/internal/logging"
        "io"
        "log"
        "net/http"
        "net/url"
        "strconv"
        "strings"
        "sync"
        "time"
)

// Bot is the interactive Slack bot.

type Bot struct {
        signingSecret string
        botToken      string // OAuth bot token for posting replies (xoxb-...)
        botUserID     string // Sentinel's Slack user ID (for @mention parsing)
        paused        bool
        pausedUntil   time.Time
        pauseReason   string
        mu            sync.RWMutex
        investigate   InvestigateFunc
        listIncidents ListIncidentsFunc
        httpClient    *http.Client
}

// InvestigateFunc manually triggers an investigation.
// Same signature as api.InvestigateFunc.

type InvestigateFunc func(service, namespace, reason string) (string, error)

// ListIncidentsFunc lists recent incidents.
// Returns slice of maps with keys: incident_id, service, status, created_at.

type ListIncidentsFunc func(ctx context.Context, limit int) ([]map[string]any, error)

// New creates a Slack bot.

func New(signingSecret, botUserID string, investigate InvestigateFunc, listIncidents ListIncidentsFunc) *Bot {
        return &Bot{
                signingSecret: signingSecret,
                botUserID:     botUserID,
                investigate:   investigate,
                listIncidents: listIncidents,
                httpClient:    &http.Client{Timeout: 10 * time.Second},
        }
}

// SetBotToken sets the Slack OAuth bot token (xoxb-...) for posting replies.
// @mentions are logged but not posted back to Slack.

func (b *Bot) SetBotToken(token string) {
        b.botToken = token
}

// IsPaused returns true if autonomous mode is paused.
// (auto-resume). The old RLock→RUnlock→Lock→Unlock→RLock dance created a race
// window where readers could see stale paused-but-expired state.

func (b *Bot) IsPaused() bool {
        b.mu.Lock()
        defer b.mu.Unlock()
        if b.paused && time.Now().Before(b.pausedUntil) {
                return true
        }
        // Auto-resume if pause expired

        if b.paused && time.Now().After(b.pausedUntil) {
                b.paused = false
                b.pauseReason = ""
        }
        return false
}

// Pause pauses autonomous mode for the given duration.

func (b *Bot) Pause(duration time.Duration, reason string) {
        b.mu.Lock()
        defer b.mu.Unlock()
        b.paused = true
        b.pausedUntil = time.Now().Add(duration)
        b.pauseReason = reason
        logging.Info("autonomous mode paused for  (reason: )", "value", duration, reason)
}

// Resume resumes autonomous mode immediately.

func (b *Bot) Resume() {
        b.mu.Lock()
        defer b.mu.Unlock()
        b.paused = false
        b.pauseReason = ""
        logging.Info("autonomous mode resumed")
}

// HandleEvents is the HTTP handler for POST /slack/events.

func (b *Bot) HandleEvents(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
                http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
                return
        }

        // unauthenticated DoS (10GB POST would OOM the 256Mi pod).

        r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB
        body, err := io.ReadAll(r.Body)
        if err != nil {
                http.Error(w, "read body failed", http.StatusBadRequest)
                return
        }

        // Slack sends: X-Slack-Signature (HMAC of body using signing secret + timestamp)
        // and X-Slack-Request-Timestamp.
        // accepted, allowing unauthenticated pause/investigate commands).
        // URL verification challenges are still accepted without a secret so initial
        // Slack app setup works (the secret may not be configured yet at install time).

        if b.signingSecret == "" {
                // Allow url_verification challenges (Slack sends these during app setup)

                var probe struct {
                        Type string `json:"type"`
                }
                if json.Unmarshal(body, &probe) == nil && probe.Type == "url_verification" {
                        logging.Info("accepting url_verification without signature (signing secret not yet configured)")
                } else {
                        logging.Info("REJECTED request: signing secret is empty (configure SENTINEL_SLACK_SIGNING_SECRET to enable)")
                        http.Error(w, "slack signing secret not configured", http.StatusServiceUnavailable)
                        return
                }
        } else if !b.verifySlackSignature(r, body) {
                logging.Error("signature verification failed")
                http.Error(w, "invalid signature", http.StatusUnauthorized)
                return
        }

        var challenge struct {
                Type      string `json:"type"`
                Challenge string `json:"challenge"`
        }
        if err := json.Unmarshal(body, &challenge); err == nil && challenge.Type == "url_verification" {
                w.Header().Set("Content-Type", "application/json")
                w.Write(fmt.Appendf(nil, `{"challenge":%q}`, challenge.Challenge))
                return
        }

        // Parse as either a slash command or an event callback.

        contentType := r.Header.Get("Content-Type")
        if strings.Contains(contentType, "application/x-www-form-urlencoded") {
                b.handleSlashCommand(w, r, body)
                return
        }

        b.handleEvent(w, r, body)
}

// verifySlackSignature verifies the HMAC-SHA256 signature Slack sends.
// the check to "avoid clock skew issues in test environments" — that left
// the bot vulnerable to indefinite replay attacks (a captured Slack request
// could be re-sent forever to trigger investigations). The freshness window
// is 5 minutes, matching Slack's own retry policy. Clock skew of more than
// 5 minutes between Sentinel and Slack's servers is a real ops problem
// worth surfacing, not papering over.

func (b *Bot) verifySlackSignature(r *http.Request, body []byte) bool {
        timestamp := r.Header.Get("X-Slack-Request-Timestamp")
        sig := r.Header.Get("X-Slack-Signature")
        if timestamp == "" || sig == "" {
                return false
        }

        // Slack's own retry policy retries failed deliveries for up to 3 hours,
        // but a *successful* delivery followed by a replay attack is the threat
        // we're defending against — 5 minutes is generous enough for normal
        // network latency and tight enough to make replay impractical.

        ts, err := strconv.ParseInt(timestamp, 10, 64)
        if err != nil {
                logging.Info("invalid X-Slack-Request-Timestamp", "value", timestamp, err)
                return false
        }
        age := time.Now().Unix() - ts
        if age > 300 {
                logging.Info("rejecting Slack request with stale timestamp (age s > 300s) — possible replay attack", "age", age)
                return false
        }
        // Also reject future timestamps (clock skew > 60s in the other direction).

        if age < -60 {
                logging.Info("rejecting Slack request with future timestamp (age s) — clock skew or attack", "age", age)
                return false
        }

        // Compute: HMAC-SHA256(signingSecret, "v0:timestamp:body")

        base := "v0:" + timestamp + ":" + string(body)
        mac := hmac.New(sha256.New, []byte(b.signingSecret))
        mac.Write([]byte(base))
        expected := "v0=" + hex.EncodeToString(mac.Sum(nil))

        return hmac.Equal([]byte(sig), []byte(expected))
}

// handleSlashCommand processes a slash command like /sentinel investigate api.

func (b *Bot) handleSlashCommand(w http.ResponseWriter, r *http.Request, body []byte) {
        // Parse form data manually (we already read the body)

        values, err := parseFormBody(string(body))
        if err != nil {
                http.Error(w, "parse form failed", http.StatusBadRequest)
                return
        }

        text := values["text"]
        command := values["command"]
        userName := values["user_name"]

        logging.Info("slash command from @", "value", userName, command, text)

        response := b.dispatchCommand(text, userName)
        w.Header().Set("Content-Type", "application/json")
        w.Write(fmt.Appendf(nil, `{"text":%q}`, response))
}

// handleEvent processes an Events API callback (app_mention).

func (b *Bot) handleEvent(w http.ResponseWriter, r *http.Request, body []byte) {
        var event struct {
                Type  string `json:"type"`
                Event struct {
                        Type    string `json:"type"`
                        Text    string `json:"text"`
                        User    string `json:"user"`
                        Channel string `json:"channel"`
                } `json:"event"`
        }
        if err := json.Unmarshal(body, &event); err != nil {
                http.Error(w, "parse event failed", http.StatusBadRequest)
                return
        }

        // Only respond to app_mention events (not message.bot_message, etc.)

        if event.Type != "event_callback" || event.Event.Type != "app_mention" {
                w.WriteHeader(http.StatusOK)
                return
        }

        text := event.Event.Text
        // Strip the @sentinel mention

        if b.botUserID != "" {
                text = strings.ReplaceAll(text, "<@"+b.botUserID+">", "")
                text = strings.ReplaceAll(text, "<@"+b.botUserID+"> ", "")
        }
        text = strings.TrimSpace(text)

        userName := "<@" + event.Event.User + ">"
        response := b.dispatchCommand(text, userName)

        if b.botToken != "" && event.Event.Channel != "" {
                if err := b.postMessage(event.Event.Channel, response); err != nil {
                        logging.Error("failed to post reply", "err", err)
                }
        } else {
                // No bot token — log the reply (dev mode)

                log.Printf("[slackbot] reply to %s in %s (no bot token — not posted): %s",
                        userName, event.Event.Channel, response)
        }

        w.WriteHeader(http.StatusOK)
}

// postMessage posts a message to a Slack channel via chat.postMessage.

func (b *Bot) postMessage(channel, text string) error {
        payload, _ := json.Marshal(map[string]string{
                "channel": channel,
                "text":    text,
        })

        req, err := http.NewRequest("POST", "https://slack.com/api/chat.postMessage", bytes.NewBuffer(payload))
        if err != nil {
                return err
        }
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("Authorization", "Bearer "+b.botToken)

        resp, err := b.httpClient.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()

        if resp.StatusCode != 200 {
                return fmt.Errorf("slack API returned %d", resp.StatusCode)
        }
        return nil
}

// dispatchCommand parses the command text and dispatches to the right handler.

func (b *Bot) dispatchCommand(text, userName string) string {
        text = strings.TrimSpace(text)
        if text == "" {
                return b.helpText()
        }

        lower := strings.ToLower(text)

        switch {
        case strings.HasPrefix(lower, "pause"):
                return b.handlePause(text, userName)
        case strings.HasPrefix(lower, "resume"):
                b.Resume()
                return "Autonomous mode resumed. Sentinel will investigate incidents automatically."
        case strings.HasPrefix(lower, "investigate"):
                return b.handleInvestigate(text, userName)
        case strings.HasPrefix(lower, "show me") || strings.HasPrefix(lower, "list") || strings.HasPrefix(lower, "incidents"):
                return b.handleList(text, userName)
        case strings.HasPrefix(lower, "status"):
                return b.handleStatus()
        case strings.HasPrefix(lower, "help"):
                return b.helpText()
        default:
                return fmt.Sprintf("Unknown command: %q\n\n%s", text, b.helpText())
        }
}

// handlePause parses "pause for X" / "pause for X minutes" / "pause Xmin".

func (b *Bot) handlePause(text, userName string) string {
        // Extract duration from text like "pause for 30 minutes" or "pause 30m"

        lower := strings.ToLower(text)
        minutes := extractMinutes(lower)
        if minutes == 0 {
                minutes = 30 // default 30 min
        }
        duration := time.Duration(minutes) * time.Minute
        b.Pause(duration, "paused by "+userName)
        return fmt.Sprintf("Autonomous mode paused for %d minutes by %s. "+
                "Sentinel will NOT investigate incidents during this time. "+
                "Say 'resume' to restart.", minutes, userName)
}

// extractMinutes pulls a number from strings like "pause for 30 minutes",
// "pause 30m", "pause 1 hour", "pause 2 hours".

func extractMinutes(s string) int {
        // Look for "hour" or "hours"

        if strings.Contains(s, "hour") {
                // Extract the number before "hour"

                parts := strings.Fields(s)
                for i, p := range parts {
                        if strings.Contains(p, "hour") && i > 0 {
                                if n, err := strconv.Atoi(parts[i-1]); err == nil {
                                        return n * 60
                                }
                        }
                }
        }
        // Look for "min", "minutes", or "m" (as a standalone token like "30m")

        parts := strings.Fields(s)
        for i, p := range parts {
                if (strings.Contains(p, "min") || strings.Contains(p, "minute")) && i > 0 {
                        if n, err := strconv.Atoi(parts[i-1]); err == nil {
                                return n
                        }
                }
                // Handle "30m" — extract the number before the 'm'

                if len(p) > 1 && strings.HasSuffix(p, "m") {
                        if n, err := strconv.Atoi(strings.TrimSuffix(p, "m")); err == nil {
                                return n
                        }
                }
        }
        // Just a bare number

        for _, p := range parts {
                if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 10000 {
                        return n
                }
        }
        return 0
}

// handleInvestigate parses "investigate <service>".

func (b *Bot) handleInvestigate(text, userName string) string {
        // Strip "investigate" prefix

        service := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(text), "investigate"))
        if service == "" {
                return "Usage: `investigate <service>` — manually triggers an investigation."
        }
        if b.investigate == nil {
                return "Investigation function not configured."
        }
        clusterID, err := b.investigate(service, "default", "manual trigger via Slack by "+userName)
        if err != nil {
                return fmt.Sprintf("Failed to trigger investigation for %s: %v", service, err)
        }
        return fmt.Sprintf("Investigation triggered for service `%s` by %s. Cluster ID: `%s`. "+
                "Sentinel will diagnose and open a PR if it finds a fix.", service, userName, clusterID)
}

// handleList parses "show me the last 5 incidents" or "list incidents".

func (b *Bot) handleList(text, userName string) string {
        if b.listIncidents == nil {
                return "Incident listing not configured."
        }
        // Extract the number (default 5)

        limit := 5
        lower := strings.ToLower(text)
        parts := strings.FieldsSeq(lower)
        for p := range parts {
                if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 50 {
                        limit = n
                        break
                }
        }
        listCtx, listCancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer listCancel()
        incidents, err := b.listIncidents(listCtx, limit)
        if err != nil {
                return fmt.Sprintf("Failed to list incidents: %v", err)
        }
        if len(incidents) == 0 {
                return "No incidents found."
        }
        var b2 strings.Builder
        b2.WriteString(fmt.Sprintf("*Last %d incidents:*\n", len(incidents)))
        for _, inc := range incidents {
                id, _ := inc["incident_id"].(string)
                service, _ := inc["service"].(string)
                status, _ := inc["status"].(string)
                b2.WriteString(fmt.Sprintf("• `%s` — %s (%s)\n", id, service, status))
        }
        return b2.String()
}

// handleStatus returns the current autonomous mode status.

func (b *Bot) handleStatus() string {
        if b.IsPaused() {
                remaining := time.Until(b.pausedUntil).Round(time.Minute)
                return fmt.Sprintf("Autonomous mode: *PAUSED* (resumes in %v)\nReason: %s",
                        remaining, b.pauseReason)
        }
        return "Autonomous mode: *ACTIVE* — Sentinel is watching for incidents and will investigate automatically."
}

// helpText returns the help message.

func (b *Bot) helpText() string {
        return "*Sentinel Slack Bot*\n\n" +
                "Commands:\n" +
                "• `pause for X minutes` — pause autonomous mode for X minutes\n" +
                "• `resume` — resume autonomous mode immediately\n" +
                "• `investigate <service>` — manually trigger investigation for a service\n" +
                "• `show me the last N incidents` — list recent incidents (default 5)\n" +
                "• `status` — show current autonomous mode status\n" +
                "• `help` — show this message\n\n" +
                "Example: \"@sentinel pause for 60 minutes\""
}

// parseFormBody parses a URL-encoded form body into a map.
// with stdlib url.ParseQuery. The old parser mis-parsed @mentions (%40),
// paths (%2F), = (%3D), & (%26), and any other encoded char — corrupting
// Slack commands that contained them.

func parseFormBody(body string) (map[string]string, error) {
        result := map[string]string{}
        if body == "" {
                return result, nil
        }
        values, err := url.ParseQuery(body)
        if err != nil {
                return result, err
        }
        for key, vals := range values {
                if len(vals) > 0 {
                        result[key] = vals[0] // take first value (Slack commands are single-valued)
                }
        }
        return result, nil
}
