// Package notify tells people what Sentinel needs from them or did: an
// action waiting for approval, an action that failed or did not clear its
// incident, an incident Sentinel has no safe action for, and an action that
// cleared one. Notices carry names, classes and decisions, never log text,
// which comes from workloads and is not to be trusted.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/time/rate"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// Kinds of notice, from the most to the least urgent.
const (
	KindApproval   = "approval"
	KindEscalation = "escalation"
	KindActed      = "acted"
)

// Notice is one message to people.
type Notice struct {
	Kind     string
	Incident model.ID
	Workload model.ObjectRef
	// Title is one line; Detail adds what to do or what happened.
	Title  string
	Detail string
}

// Slack posts notices to a Slack incoming webhook, or to anything that takes
// Slack's {"text": ...} payload. It never blocks its caller: notices are sent
// in the background, at most a few a minute, and failures are logged.
type Slack struct {
	URL     string
	Client  *http.Client
	Limiter *rate.Limiter
	Log     *slog.Logger
}

// NewSlack returns a notifier for a webhook URL, sending at most six notices
// a minute.
func NewSlack(url string, log *slog.Logger) *Slack {
	return &Slack{URL: url, Client: &http.Client{Timeout: 10 * time.Second}, Limiter: rate.NewLimiter(rate.Every(10*time.Second), 6),
		Log: log}
}

var emoji = map[string]string{KindApproval: ":raised_hand:", KindEscalation: ":rotating_light:", KindActed: ":white_check_mark:"}

// Notify sends a notice in the background.
func (s *Slack) Notify(ctx context.Context, n Notice) {
	if !s.Limiter.Allow() {
		s.Log.Warn("too many notices; dropped one", "kind", n.Kind, "incident", n.Incident)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := s.send(ctx, n); err != nil {
			s.Log.Error("could not send a notice", "kind", n.Kind, "incident", n.Incident, "error", err)
		}
	}()
}

func (s *Slack) send(ctx context.Context, n Notice) error {
	text := fmt.Sprintf("%s *%s*", emoji[n.Kind], n.Title)
	if n.Detail != "" {
		text += "\n" + n.Detail
	}
	text += fmt.Sprintf("\nIncident `%s` · `%s`", n.Incident, n.Workload.String())
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("webhook answered %s: %s", res.Status, msg)
	}
	return nil
}
