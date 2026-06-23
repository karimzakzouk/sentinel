package agents

import (
        "bytes"
        "encoding/json"
        "fmt"
        "log"
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
                log.Printf("[alert] %s/%s — %s", namespace, service, reason)
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
                log.Printf("[alert] Slack webhook failed: %v — logging instead", err)
                log.Printf("[alert] %s/%s — %s", namespace, service, reason)
                return
        }
        resp.Body.Close()
        log.Printf("[alert] Slack notification sent for %s/%s", namespace, service)
}
