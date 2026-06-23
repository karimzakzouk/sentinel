package agents

import (
        "context"
        "encoding/json"
        "fmt"
        "strings"
        "sync"
        "time"

        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

        "github.com/karimzakzouk/sentinel/v3/internal/k8s"
        "github.com/karimzakzouk/sentinel/v3/internal/llm"
        "github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

const evidenceSystemPrompt = `You are Sentinel's Evidence Gatherer agent.

You are given a list of root cause hypotheses. For each hypothesis, decide:
  1. What query would *support* this hypothesis? (e.g. a Prometheus query for high connection count, a Loki log query for the specific error message)
  2. What query would *contradict* this hypothesis?

You can query:
  - Loki (log search): {"type": "loki", "query": "{service=\"X\",namespace=\"Y\"} |= \"keyword\""}
  - Prometheus (metrics): {"type": "prometheus", "query": "sum(rate(http_requests_total{status=\"500\"}[5m]))"}
  - Jaeger (traces): {"type": "jaeger", "query": "service:X operation:Y tags:error=true"}
  - Kubernetes (events): {"type": "k8s", "query": "namespace:Y kind:Pod reason:BackOff"}

Respond with a JSON object of shape:
{
  "evidence_queries": [
    {
      "hypothesis_id": "H1",
      "supports_or_contradicts": "supports|contradicts",
      "query": { ... },
      "expectation": "what we expect to see if hypothesis is true"
    }
  ],
  "rewind": null
}

Set "rewind" ONLY if you discover a brand-new fact that invalidates the entire hypothesis set (e.g. a hard upstream dependency is down). Leave it null 99% of the time.`

// EvidenceGatherer generates queries, executes them, and attaches evidence to each hypothesis.
type EvidenceGatherer struct {
        router *llm.Router
        k8s    *k8s.Client
}

// NewEvidenceGatherer creates an EvidenceGatherer.
func NewEvidenceGatherer(router *llm.Router, k8sClient *k8s.Client) *EvidenceGatherer {
        return &EvidenceGatherer{router: router, k8s: k8sClient}
}

// Run executes the evidence gatherer agent.
func (a *EvidenceGatherer) Run(ctx context.Context, state *schemas.AgentState) error {
        if len(state.Hypotheses) == 0 {
                return nil
        }

        userMsg := a.buildUserMessage(state)
        result, err := a.router.FastJSON(ctx, evidenceSystemPrompt, userMsg, 4000)
        if err != nil {
                // Don't fail the whole investigation — continue with no evidence.
                // The Analyst will pick the top hypothesis by prior confidence.
                state.Evidence = nil
                state.AgentTrace = append(state.AgentTrace, map[string]any{
                        "agent":  "evidence_gatherer",
                        "action": "llm_failed_no_evidence",
                        "error":  fmt.Sprintf("%v", err)[:200],
                })
                return nil
        }

        // Check for rewind
        if rewind, ok := result["rewind"].(map[string]any); ok && len(rewind) > 0 {
                reason, _ := rewind["reason"].(string)
                newCtx, _ := rewind["new_context"].(map[string]any)
                ev := schemas.RewindEvent{
                        RaisedBy:   "evidence_gatherer",
                        Reason:     reason,
                        NewContext: newCtx,
                        Ts:         time.Now().UTC(),
                }
                state.RewindEvents = append(state.RewindEvents, ev)
                state.Hypotheses = nil // wipe — Investigator will re-run
                state.AgentTrace = append(state.AgentTrace, map[string]any{
                        "agent":  "evidence_gatherer",
                        "action": "rewind",
                        "reason": reason,
                })
                return nil
        }

        // Execute all queries in parallel (with per-query timeout)
        queries, _ := result["evidence_queries"].([]any)
        var (
                mu       sync.Mutex
                evidence []schemas.Evidence
                wg       sync.WaitGroup
        )

        for i, q := range queries {
                qMap, ok := q.(map[string]any)
                if !ok {
                        continue
                }
                wg.Add(1)
                go func(idx int, qm map[string]any) {
                        defer wg.Done()
                        ev := a.runSingleQuery(ctx, idx, qm)
                        mu.Lock()
                        evidence = append(evidence, ev)
                        mu.Unlock()
                }(i, qMap)
        }
        wg.Wait()

        state.Evidence = evidence
        state.AgentTrace = append(state.AgentTrace, map[string]any{
                "agent":          "evidence_gatherer",
                "evidence_count": len(evidence),
        })
        return nil
}

func (a *EvidenceGatherer) buildUserMessage(state *schemas.AgentState) string {
        var parts []string
        parts = append(parts, fmt.Sprintf("# Service: %s (namespace: %s)", state.Cluster.Service, state.Cluster.Namespace))
        parts = append(parts, "")
        parts = append(parts, "## Hypotheses to test:")
        for _, h := range state.Hypotheses {
                parts = append(parts, fmt.Sprintf("### %s (confidence=%.2f)", h.HID, h.Confidence))
                parts = append(parts, h.Statement)
                if len(h.SupportingEvidence) > 0 {
                        parts = append(parts, "Supporting evidence so far:")
                        for _, e := range h.SupportingEvidence {
                                parts = append(parts, fmt.Sprintf("  - %s", e))
                        }
                }
                parts = append(parts, "")
        }
        parts = append(parts, "## Cluster context (for query construction):")
        parts = append(parts, state.Cluster.Summary)
        parts = append(parts, "")
        parts = append(parts, "## Your Task")
        parts = append(parts, "Produce evidence queries for each hypothesis. Be concrete — use the actual service name and namespace.")
        return strings.Join(parts, "\n")
}

func (a *EvidenceGatherer) runSingleQuery(ctx context.Context, idx int, q map[string]any) schemas.Evidence {
        query, _ := q["query"].(map[string]any)
        qtype, _ := query["type"].(string)
        hypID, _ := q["hypothesis_id"].(string)
        supportsOrContradicts, _ := q["supports_or_contradicts"].(string)

        queryJSON, _ := json.Marshal(query)
        ev := schemas.Evidence{
                EID:   fmt.Sprintf("E%d", idx+1),
                Source: qtype,
                Query: string(queryJSON),
                Ts:    time.Now().UTC(),
        }
        if supportsOrContradicts == "supports" {
                ev.SupportsHypothesisID = &hypID
        } else if supportsOrContradicts == "contradicts" {
                ev.ContradictsHypothesisID = &hypID
        }

        // Execute with 15s timeout
        queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
        defer cancel()

        result := a.executeQuery(queryCtx, query)
        ev.Result = result
        return ev
}

// executeQuery dispatches to the right backend based on query type.
func (a *EvidenceGatherer) executeQuery(ctx context.Context, query map[string]any) map[string]any {
        qtype, _ := query["type"].(string)
        q, _ := query["query"].(string)

        switch qtype {
        case "k8s":
                return a.queryK8s(ctx, q)
        case "loki", "prometheus", "jaeger":
                // These need external URLs — return error if not configured.
                // v3.1 will wire these via the LLM provider's observability stack.
                return map[string]any{"error": fmt.Sprintf("%s_url not configured", qtype)}
        default:
                return map[string]any{"error": fmt.Sprintf("unknown query type: %s", qtype)}
        }
}

// queryK8s queries the K8s API for events, pods, deployments, or logs.
func (a *EvidenceGatherer) queryK8s(ctx context.Context, query string) map[string]any {
        // Parse the query: "namespace:N kind:K [labelSelector:K=V] [fieldSelector:K=V] [limit:N]"
        parts := parseQueryParts(query)
        namespace, _ := parts["namespace"]
        if namespace == "" {
                namespace = "default"
        }
        kind, _ := parts["kind"]
        limit := int64(50)
        if l, ok := parts["limit"]; ok {
                fmt.Sscanf(l, "%d", &limit)
        }

        listOpts := metav1.ListOptions{Limit: limit}
        if ls, ok := parts["labelSelector"]; ok {
                listOpts.LabelSelector = ls
        }
        if fs, ok := parts["fieldSelector"]; ok {
                listOpts.FieldSelector = fs
        }

        cs := a.k8s.Clientset()
        switch strings.ToLower(kind) {
        case "event", "events":
                events, err := cs.CoreV1().Events(namespace).List(ctx, listOpts)
                if err != nil {
                        return map[string]any{"error": err.Error()}
                }
                items := make([]map[string]any, 0, len(events.Items))
                for i, e := range events.Items {
                        if int64(i) >= limit {
                                break
                        }
                        items = append(items, map[string]any{
                                "name":            e.Name,
                                "reason":          e.Reason,
                                "message":         e.Message,
                                "count":           e.Count,
                                "involved_object": fmt.Sprintf("%s/%s", e.InvolvedObject.Kind, e.InvolvedObject.Name),
                                "type":            e.Type,
                        })
                }
                return map[string]any{"items": items, "count": len(items)}
        case "pod", "pods":
                pods, err := cs.CoreV1().Pods(namespace).List(ctx, listOpts)
                if err != nil {
                        return map[string]any{"error": err.Error()}
                }
                items := make([]map[string]any, 0, len(pods.Items))
                for i, p := range pods.Items {
                        if int64(i) >= limit {
                                break
                        }
                        restarts := 0
                        for _, cs := range p.Status.ContainerStatuses {
                                restarts += int(cs.RestartCount)
                        }
                        items = append(items, map[string]any{
                                "name":          p.Name,
                                "phase":         string(p.Status.Phase),
                                "restart_count": restarts,
                                "node":          p.Spec.NodeName,
                        })
                }
                return map[string]any{"items": items, "count": len(items)}
        case "deployment", "deployments":
                deps, err := cs.AppsV1().Deployments(namespace).List(ctx, listOpts)
                if err != nil {
                        return map[string]any{"error": err.Error()}
                }
                items := make([]map[string]any, 0, len(deps.Items))
                for i, d := range deps.Items {
                        if int64(i) >= limit {
                                break
                        }
                        containers := []string{}
                        for _, c := range d.Spec.Template.Spec.Containers {
                                containers = append(containers, c.Image)
                        }
                        items = append(items, map[string]any{
                                "name":           d.Name,
                                "replicas":       d.Spec.Replicas,
                                "ready_replicas": d.Status.ReadyReplicas,
                                "image":          containers,
                        })
                }
                return map[string]any{"items": items, "count": len(items)}
        default:
                return map[string]any{"error": fmt.Sprintf("unsupported k8s kind: %q. Use 'event', 'pod', or 'deployment'.", kind)}
        }
}

// parseQueryParts parses "key:value key:value" pairs into a map.
func parseQueryParts(s string) map[string]string {
        out := map[string]string{}
        for _, part := range strings.Fields(s) {
                if idx := strings.Index(part, ":"); idx > 0 {
                        out[part[:idx]] = part[idx+1:]
                }
        }
        return out
}
