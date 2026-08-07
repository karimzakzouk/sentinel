package slackbot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Bot is the interactive Slack bot. It owns the autonomous-mode pause
// state, dispatches commands from @mentions and slash commands, and posts
// replies via the Slack chat.postMessage API when a bot OAuth token is
// configured.
type Bot struct {
	signingSecret string
	botToken      string
	botUserID     string

	paused      bool
	pausedUntil time.Time
	pauseReason string
	mu          sync.Mutex

	investigate   InvestigateFunc
	listIncidents ListIncidentsFunc
	httpClient    *http.Client
}

// InvestigateFunc manually triggers an investigation.
type InvestigateFunc func(service, namespace, reason string) (string, error)

// ListIncidentsFunc lists recent incidents. Returns a slice of maps with
// keys: incident_id, service, status, created_at.
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

// SetBotToken sets the Slack OAuth bot token (xoxb-...) for posting
// replies. Without a token, @mentions are dispatched but no reply is
// posted to Slack (dev mode — the reply is logged instead).
func (b *Bot) SetBotToken(token string) {
	b.botToken = token
}

// IsPaused reports whether autonomous mode is currently paused. If the
// pause has expired, the bot auto-resumes and returns false. The mutex is
// held across the read + auto-resume to avoid the read-modify-write race
// where readers could see stale paused-but-expired state.
func (b *Bot) IsPaused() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.paused && time.Now().Before(b.pausedUntil) {
		return true
	}
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
	logging.Info("slackbot: autonomous mode paused", "duration", duration, "reason", reason)
}

// Resume resumes autonomous mode immediately.
func (b *Bot) Resume() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused = false
	b.pauseReason = ""
	logging.Info("slackbot: autonomous mode resumed")
}

// PausedUntil returns when the current pause expires (zero if not paused).
// Used by handleStatus to show the remaining time.
func (b *Bot) PausedUntil() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pausedUntil
}

// PauseReason returns the reason the bot was paused (empty if not paused).
func (b *Bot) PauseReason() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pauseReason
}

// HandleEvents is the HTTP handler for POST /slack/events.
func (b *Bot) HandleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Cap the body at 1MB to avoid unauthenticated DoS.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// Slack's signing secret may not be configured at install time (the
	// url_verification challenge is sent during app setup). Accept
	// url_verification without a signature; reject everything else.
	if b.signingSecret == "" {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(body, &probe) == nil && probe.Type == "url_verification" {
			logging.Info("slackbot: accepting url_verification without signature (signing secret not yet configured)")
		} else {
			logging.Info("slackbot: rejecting request — signing secret not configured")
			http.Error(w, "slack signing secret not configured", http.StatusServiceUnavailable)
			return
		}
	} else if !b.verifySlackSignature(r, body) {
		logging.Error("slackbot: signature verification failed")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var challenge struct {
		Type      string `json:"type"`
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(body, &challenge); err == nil && challenge.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fmt.Appendf(nil, `{"challenge":%q}`, challenge.Challenge))
		return
	}

	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/x-www-form-urlencoded") {
		b.handleSlashCommand(w, r, body)
		return
	}

	b.handleEvent(w, r, body)
}

// verifySlackSignature verifies the HMAC-SHA256 signature Slack sends.
// Stale timestamps (>5 min old) are rejected to prevent replay attacks;
// future timestamps (>60s ahead) are rejected to surface clock skew.
func (b *Bot) verifySlackSignature(r *http.Request, body []byte) bool {
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	sig := r.Header.Get("X-Slack-Signature")
	if timestamp == "" || sig == "" {
		return false
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		logging.Info("slackbot: invalid X-Slack-Request-Timestamp", "timestamp", timestamp, "err", err)
		return false
	}
	age := time.Now().Unix() - ts
	if age > 300 {
		logging.Info("slackbot: rejecting stale Slack request — possible replay", "age_sec", age)
		return false
	}
	if age < -60 {
		logging.Info("slackbot: rejecting future Slack request — clock skew or attack", "age_sec", age)
		return false
	}

	base := "v0:" + timestamp + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(b.signingSecret))
	mac.Write([]byte(base))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(sig), []byte(expected))
}

// handleSlashCommand processes a slash command like /sentinel investigate api.
func (b *Bot) handleSlashCommand(w http.ResponseWriter, r *http.Request, body []byte) {
	values, err := parseFormBody(string(body))
	if err != nil {
		http.Error(w, "parse form failed", http.StatusBadRequest)
		return
	}

	text := values["text"]
	command := values["command"]
	userName := values["user_name"]

	logging.Info("slackbot: slash command",
		"user", userName, "command", command, "text", text)

	response := b.dispatchCommand(text, userName)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(fmt.Appendf(nil, `{"text":%q}`, response))
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

	if event.Type != "event_callback" || event.Event.Type != "app_mention" {
		w.WriteHeader(http.StatusOK)
		return
	}

	text := event.Event.Text
	if b.botUserID != "" {
		text = strings.ReplaceAll(text, "<@"+b.botUserID+">", "")
		text = strings.ReplaceAll(text, "<@"+b.botUserID+"> ", "")
	}
	text = strings.TrimSpace(text)

	userName := "<@" + event.Event.User + ">"
	response := b.dispatchCommand(text, userName)

	if b.botToken != "" && event.Event.Channel != "" {
		if err := b.postMessage(event.Event.Channel, response); err != nil {
			logging.Error("slackbot: failed to post reply", "err", err)
		}
	} else {
		// No bot token — log the reply (dev mode).
		logging.Info("slackbot: reply not posted (no bot token)",
			"user", userName, "channel", event.Event.Channel, "reply", response)
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

// dispatchCommand parses the command text and dispatches to the right
// handler.
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
	lower := strings.ToLower(text)
	minutes := extractMinutes(lower)
	if minutes == 0 {
		minutes = 30
	}
	duration := time.Duration(minutes) * time.Minute
	b.Pause(duration, "paused by "+userName)
	return fmt.Sprintf("Autonomous mode paused for %d minutes by %s. "+
		"Sentinel will NOT investigate incidents during this time. "+
		"Say 'resume' to restart.", minutes, userName)
}

// extractMinutes pulls a number from strings like "pause for 30 minutes",
// "pause 30m", "pause 1 hour", "pause 2 hours". Returns 0 if no number
// is found.
func extractMinutes(s string) int {
	if strings.Contains(s, "hour") {
		parts := strings.Fields(s)
		for i, p := range parts {
			if strings.Contains(p, "hour") && i > 0 {
				if n, err := strconv.Atoi(parts[i-1]); err == nil {
					return n * 60
				}
			}
		}
	}
	parts := strings.Fields(s)
	for i, p := range parts {
		if (strings.Contains(p, "min") || strings.Contains(p, "minute")) && i > 0 {
			if n, err := strconv.Atoi(parts[i-1]); err == nil {
				return n
			}
		}
		// Handle "30m" — extract the number before the 'm'.
		if len(p) > 1 && strings.HasSuffix(p, "m") {
			if n, err := strconv.Atoi(strings.TrimSuffix(p, "m")); err == nil {
				return n
			}
		}
	}
	// Bare number.
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 10000 {
			return n
		}
	}
	return 0
}

// handleInvestigate parses "investigate <service>".
func (b *Bot) handleInvestigate(text, userName string) string {
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
	limit := 5
	lower := strings.ToLower(text)
	for p := range strings.FieldsSeq(lower) {
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
		remaining := time.Until(b.PausedUntil()).Round(time.Minute)
		return fmt.Sprintf("Autonomous mode: *PAUSED* (resumes in %v)\nReason: %s",
			remaining, b.PauseReason())
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

// parseFormBody parses a URL-encoded form body into a map. Uses stdlib
// url.ParseQuery so encoded characters (@mentions, paths, =, &) are
// handled correctly.
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
			result[key] = vals[0]
		}
	}
	return result, nil
}
