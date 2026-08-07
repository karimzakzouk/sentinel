package agents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// AlertSender sends Slack-formatted alerts. If no webhook URL is configured,
// alerts are logged instead (graceful degradation).
type AlertSender struct {
	webhookURL string
	httpClient *http.Client
}

// NewAlertSender returns an AlertSender. webhookURL may be empty.
func NewAlertSender(webhookURL string) *AlertSender {
	return &AlertSender{
		webhookURL: webhookURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// SendAlert sends an alert about a detected incident. If prURL is non-empty,
// the alert is colored green ("fixed"); otherwise red ("incident"). When no
// webhook is configured, the alert is logged instead.
func (a *AlertSender) SendAlert(service, namespace, reason, prURL string) {
	if a.webhookURL == "" {
		logging.Info("incident alert (no webhook configured)",
			"service", service, "namespace", namespace, "reason", reason, "pr_url", prURL)
		return
	}

	color := "danger"
	icon := "[!]"
	if prURL != "" {
		color = "good"
		icon = "[v]"
	}

	fields := []map[string]any{
		{
			"title": "Service",
			"value": fmt.Sprintf("`%s` (`%s`)", service, namespace),
			"short": true,
		},
		{
			"title": "Reason",
			"value": reason,
			"short": false,
		},
	}
	if prURL != "" {
		fields = append(fields, map[string]any{
			"title": "Pull Request",
			"value": prURL,
			"short": false,
		})
	}

	payload := map[string]any{
		"attachments": []map[string]any{
			{
				"color":  color,
				"title":  fmt.Sprintf("%s Sentinel - %s", icon, service),
				"fields": fields,
				"footer": "Sentinel",
				"ts":     time.Now().Unix(),
			},
		},
	}

	body, _ := json.Marshal(payload)
	resp, err := a.httpClient.Post(a.webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		logging.Error("slack webhook failed, logging instead",
			"err", err, "service", service, "namespace", namespace, "reason", reason)
		return
	}
	resp.Body.Close()
	logging.Info("slack notification sent", "service", service, "namespace", namespace)
}
