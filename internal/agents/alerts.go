package agents

import (
	"github.com/karimzakzouk/sentinel/internal/logging"
        "bytes"
        "encoding/json"
        "fmt"
        "net/http"
        "time"
)

// AlertSender sends Slack-formatted alerts.
// If no webhook URL is configured, alerts are logged instead (graceful degradation).

type AlertSender struct {
        webhookURL string
        httpClient *http.Client
}

// NewAlertSender creates an AlertSender.

func NewAlertSender(webhookURL string) *AlertSender {
        return &AlertSender{
                webhookURL: webhookURL,
                httpClient: &http.Client{Timeout: 10 * time.Second},
        }
}

// SendAlert sends a Slack alert about a detected incident.

func (a *AlertSender) SendAlert(service, namespace, reason, prURL string) {
        if a.webhookURL == "" {
                logging.Info("/ —", "value", namespace, service, reason)
                return
        }

        color := "danger"
        icon := "🚨"
        if prURL != "" {
                color = "good"
                icon = "✅"
        }

        var fields []map[string]any
        fields = append(fields, map[string]any{
                "title": "Service",
                "value": fmt.Sprintf("`%s` (`%s`)", service, namespace),
                "short": true,
        })
        fields = append(fields, map[string]any{
                "title": "Reason",
                "value": reason,
                "short": false,
        })
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
                                "title":  fmt.Sprintf("%s Sentinel — %s", icon, service),
                                "fields": fields,
                                "footer": "Sentinel v3.5",
                                "ts":     time.Now().Unix(),
                        },
                },
        }

        body, _ := json.Marshal(payload)
        resp, err := a.httpClient.Post(a.webhookURL, "application/json", bytes.NewReader(body))
        if err != nil {
                logging.Error("Slack webhook failed:  — logging instead", "err", err)
                logging.Info("/ —", "value", namespace, service, reason)
                return
        }
        resp.Body.Close()
        logging.Info("Slack notification sent for /", "value", namespace, service)
}
