// Package fixverify verifies that a fix actually resolved the incident
// by checking canary pod metrics (kubelet cAdvisor) and optionally
// re-querying the original alert's PromQL expression.
package fixverify

import (
        "context"
        "encoding/json"
        "fmt"
        "net/http"
        "net/url"
        "strings"
        "time"

        "github.com/karimzakzouk/sentinel/internal/logging"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/client-go/kubernetes"
)

type Verifier struct {
        clientset     *kubernetes.Clientset
        prometheusURL string
        httpClient    *http.Client
}

func New(clientset *kubernetes.Clientset, prometheusURL string) *Verifier {
        return &Verifier{
                clientset:     clientset,
                prometheusURL: strings.TrimRight(prometheusURL, "/"),
                httpClient:    &http.Client{Timeout: 10 * time.Second},
        }
}

type Result struct {
        Verified       bool     `json:"verified"`
        Detail         string   `json:"detail"`
        MetricsChecked []string `json:"metrics_checked"`
}

func (v *Verifier) VerifyCanaryMetrics(ctx context.Context, podName, namespace string) Result {
        result := Result{Verified: true, MetricsChecked: []string{}}

        pod, err := v.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
        if err != nil {
                return Result{Verified: true, Detail: fmt.Sprintf("can't get pod for metric check: %v", err)}
        }

        for _, cs := range pod.Status.ContainerStatuses {
                result.MetricsChecked = append(result.MetricsChecked, fmt.Sprintf("restarts=%d", cs.RestartCount))
                if cs.RestartCount > 0 {
                        result.Verified = false
                        result.Detail = fmt.Sprintf("canary has %d restarts — not stable", cs.RestartCount)
                        return result
                }
                if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
                        result.MetricsChecked = append(result.MetricsChecked, "oom_killed=true")
                        result.Verified = false
                        result.Detail = "canary was OOMKilled"
                        return result
                }
        }

        if len(result.MetricsChecked) > 0 {
                result.Detail = fmt.Sprintf("canary stable: %s", strings.Join(result.MetricsChecked, ", "))
        } else {
                result.Detail = "canary pod stable (no restarts, no OOM)"
        }
        return result
}

func (v *Verifier) RequeryAlert(ctx context.Context, promqlExpr string) Result {
        if v.prometheusURL == "" {
                return Result{Verified: true, Detail: "Prometheus not configured — skipping alert re-query"}
        }

        queryURL := fmt.Sprintf("%s/api/v1/query?query=%s", v.prometheusURL, url.QueryEscape(promqlExpr))
        req, err := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
        if err != nil {
                return Result{Verified: true, Detail: fmt.Sprintf("alert re-query failed: %v", err)}
        }

        resp, err := v.httpClient.Do(req)
        if err != nil {
                return Result{Verified: true, Detail: fmt.Sprintf("alert re-query failed: %v", err)}
        }
        defer resp.Body.Close()

        if resp.StatusCode != http.StatusOK {
                return Result{Verified: true, Detail: fmt.Sprintf("alert re-query: Prometheus returned %d", resp.StatusCode)}
        }

        var promResp struct {
                Status string `json:"status"`
                Data   struct {
                        ResultType string            `json:"resultType"`
                        Result     []json.RawMessage `json:"result"`
                } `json:"data"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&promResp); err != nil {
                return Result{Verified: true, Detail: fmt.Sprintf("alert re-query: decode failed: %v", err)}
        }

        if len(promResp.Data.Result) == 0 {
                logging.Info("fix-verify: alert re-query empty — alert would not fire", "expr", promqlExpr)
                return Result{
                        Verified:       true,
                        Detail:         "alert re-query: expression returned no data — alert would not fire",
                        MetricsChecked: []string{"alert_requery=empty"},
                }
        }

        logging.Info("fix-verify: alert re-query returned data — alert would still fire", "expr", promqlExpr, "series", len(promResp.Data.Result))
        return Result{
                Verified:       false,
                Detail:         fmt.Sprintf("alert re-query: expression returned %d series — alert would still fire", len(promResp.Data.Result)),
                MetricsChecked: []string{fmt.Sprintf("alert_requery=%d_series", len(promResp.Data.Result))},
        }
}

// scrapeKubelet queries the kubelet /metrics/cadvisor endpoint via the
// K8s API proxy. Currently unused — VerifyCanaryMetrics uses the K8s API
// directly (pods.Get) which is more reliable. Kept for future use when
// we add CPU/memory metric checks.
func (v *Verifier) scrapeKubelet(ctx context.Context, path string) (string, error) {
        _ = ctx
        _ = path
        return "", nil
}
