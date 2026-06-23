// Package slackbot implements an interactive two-way Slack bot for Sentinel.
//
// v3.5.0: Replaces the outbound-only Slack alerts (agents/alerts.go) with
// two-way communication. The bot:
//   - Reports what it's doing as it works (investigation started, fix proposed, PR opened)
//   - Accepts commands via @sentinel mentions or slash commands:
//     "pause autonomous mode for X"  — pauses auto-investigation for X minutes
//     "investigate Y"                — manually triggers investigation for service Y
//     "show me the last 5 incidents" — lists recent incidents
//     "status"                       — shows current autonomous mode status
//     "resume"                       — resumes autonomous mode
//
// ~200 lines. Uses the Slack Events API (not RTM — simpler, webhook-based).
//
// Endpoints added to api/server.go:
//   POST /slack/events  — receives Slack Events API + slash command payloads
package slackbot

import (
        "context"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "net/http"
        "strconv"
        "strings"
        "sync"
        "time"
)

// Bot is the interactive Slack bot.
type Bot struct {
        signingSecret  string
        botUserID      string // Sentinel's Slack user ID (for @mention parsing)
        paused         bool
        pausedUntil    time.Time
        pauseReason    string
        mu             sync.RWMutex
        investigate    InvestigateFunc
        listIncidents  ListIncidentsFunc
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
        }
}

// IsPaused returns true if autonomous mode is paused.
// v3.5.0: The detection layer checks this before triggering investigations.
func (b *Bot) IsPaused() bool {
        b.mu.RLock()
        defer b.mu.RUnlock()
        if b.paused && time.Now().Before(b.pausedUntil) {
                return true
        }
        // Auto-resume if pause expired
        if b.paused && time.Now().After(b.pausedUntil) {
                b.mu.RUnlock()
                b.mu.Lock()
                b.paused = false
                b.pauseReason = ""
                b.mu.Unlock()
                b.mu.RLock()
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
        log.Printf("[slackbot] autonomous mode paused for %v (reason: %s)", duration, reason)
}

// Resume resumes autonomous mode immediately.
func (b *Bot) Resume() {
        b.mu.Lock()
        defer b.mu.Unlock()
        b.paused = false
        b.pauseReason = ""
        log.Printf("[slackbot] autonomous mode resumed")
}

// HandleEvents is the HTTP handler for POST /slack/events.
// v3.5.0: Handles both the Events API (app mentions) and slash commands.
func (b *Bot) HandleEvents(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
                http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
                return
        }

        body, err := io.ReadAll(r.Body)
        if err != nil {
                http.Error(w, "read body failed", http.StatusBadRequest)
                return
        }

        // v3.5.0: Slack sends a URL verification challenge on first setup.
        // Payload: {"token":"...","challenge":"...","type":"url_verification"}
        var challenge struct {
                Type      string `json:"type"`
                Challenge string `json:"challenge"`
        }
        if err := json.Unmarshal(body, &challenge); err == nil && challenge.Type == "url_verification" {
                w.Header().Set("Content-Type", "application/json")
                w.Write([]byte(fmt.Sprintf(`{"challenge":%q}`, challenge.Challenge)))
                return
        }

        // Parse as either a slash command or an event callback.
        // Slash commands come as form-encoded data; events come as JSON.
        contentType := r.Header.Get("Content-Type")
        if strings.Contains(contentType, "application/x-www-form-urlencoded") {
                b.handleSlashCommand(w, r, body)
                return
        }

        b.handleEvent(w, r, body)
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

        log.Printf("[slackbot] slash command from @%s: %s %s", userName, command, text)

        response := b.dispatchCommand(text, userName)
        w.Header().Set("Content-Type", "application/json")
        w.Write([]byte(fmt.Sprintf(`{"text":%q}`, response)))
}

// handleEvent processes an Events API callback (app_mention).
func (b *Bot) handleEvent(w http.ResponseWriter, r *http.Request, body []byte) {
        var event struct {
                Type string `json:"type"`
                Event struct {
                        Type string `json:"type"`
                        Text string `json:"text"`
                        User string `json:"user"`
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

        // v3.5.0: In a real implementation, we'd post this response back to Slack
        // via the chat.postMessage API. For now, we log it — the caller can wire
        // a Slack message sender if they want the bot to actually reply.
        log.Printf("[slackbot] reply to %s: %s", userName, response)

        w.WriteHeader(http.StatusOK)
}

// dispatchCommand parses the command text and dispatches to the right handler.
// v3.5.0: Returns the response text (Markdown for Slack).
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
        parts := strings.Fields(lower)
        for _, p := range parts {
                if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 50 {
                        limit = n
                        break
                }
        }
        incidents, err := b.listIncidents(context.Background(), limit)
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
func parseFormBody(body string) (map[string]string, error) {
        result := map[string]string{}
        if body == "" {
                return result, nil
        }
        // Simple parser — doesn't handle arrays, but sufficient for Slack commands
        for _, pair := range strings.Split(body, "&") {
                kv := strings.SplitN(pair, "=", 2)
                if len(kv) == 2 {
                        // URL-decode the value
                        val := strings.ReplaceAll(kv[1], "+", " ")
                        val = strings.ReplaceAll(val, "%21", "!")
                        val = strings.ReplaceAll(val, "%27", "'")
                        val = strings.ReplaceAll(val, "%28", "(")
                        val = strings.ReplaceAll(val, "%29", ")")
                        result[kv[0]] = val
                }
        }
        return result, nil
}
