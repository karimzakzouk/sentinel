// Package main is the Sentinel v3 single-binary entrypoint.
// Replaces: services/core + services/ingestion + services/detection +
// services/reasoning + services/safety + services/execution + services/feedback +
// the OPA sidecar + the Postgres pod + the Weaviate pod + the UI pod.
//
// One binary, one process, one pod. ~20MB image.
package main

import (
        "context"
        "flag"
        "fmt"
        "log"
        "net/http"
        "os"
        "os/signal"
        "syscall"
        "time"

        "github.com/karimzakzoukz/sentinel/v3/internal/api"
        "github.com/karimzakzoukz/sentinel/v3/internal/config"
        "github.com/karimzakzoukz/sentinel/v3/internal/feedback"
        "github.com/karimzakzoukz/sentinel/v3/internal/k8s"
        "github.com/karimzakzoukz/sentinel/v3/internal/llm"
        "github.com/karimzakzoukz/sentinel/v3/internal/schemas"
        "github.com/karimzakzoukz/sentinel/v3/internal/watcher"
)

func main() {
        // CLI flags (mostly for dev — in prod, env vars are set by Helm)
        addr := flag.String("addr", "", "HTTP listen address (default: from env)")
        dbPath := flag.String("db", "", "SQLite DB path (default: from env)")
        flag.Parse()

        cfg := config.Load()
        if *addr != "" {
                cfg.APIHost = "" // let the addr flag win
                cfg.APIPort = 0
                _ = addr // parsed but we build the addr below
        }
        if *dbPath != "" {
                cfg.SQLitePath = *dbPath
        }

        listenAddr := fmt.Sprintf("%s:%d", cfg.APIHost, cfg.APIPort)

        log.Printf("[main] Sentinel v3.0.0 starting")
        log.Printf("[main] LLM provider: %s (model: %s)", cfg.LLMProvider, cfg.LLMFrontierModel)
        log.Printf("[main] SQLite path: %s", cfg.SQLitePath)
        log.Printf("[main] API listen: %s", listenAddr)

        // 1. Initialize SQLite store (replaces Postgres + Weaviate)
        store, err := feedback.New(cfg.SQLitePath)
        if err != nil {
                log.Fatalf("[main] failed to init storage: %v", err)
        }
        defer store.Close()
        log.Printf("[main] storage initialized (SQLite at %s)", cfg.SQLitePath)

        // 2. Initialize K8s client (in-cluster)
        k8sClient, err := k8s.New()
        if err != nil {
                log.Fatalf("[main] failed to init K8s client: %v", err)
        }
        log.Printf("[main] K8s client initialized")

        // 3. Initialize LLM router
        router := llm.New(
                cfg.ResolvedBaseURL(),
                cfg.LLMAPIKey,
                cfg.LLMFastModel,
                cfg.LLMFrontierModel,
        )
        log.Printf("[main] LLM router initialized")
        _ = router // used by the agent pipeline (next commit)

        // 4. Ready state (set to true after watcher starts)
        ready := false

        // 5. Create the investigation handler
        handler := func(ctx context.Context, cluster schemas.AnomalyCluster, crName, crNamespace string) {
                log.Printf("[investigation] starting for %s/%s (service=%s)", crNamespace, crName, cluster.Service)
                incident := schemas.NewIncident(cluster)
                incident.Status = schemas.StatusInvestigating

                // Save the incident
                if err := store.UpsertIncident(ctx, &incident); err != nil {
                        log.Printf("[investigation] failed to save incident: %v", err)
                        return
                }

                // Append audit event
                _ = store.AppendEvent(ctx, incident.IncidentID, "reasoning", "investigation_started", nil,
                        map[string]any{"cluster_summary": cluster.Summary})

                // v3.0: Run the full reasoning pipeline here.
                // For now, this is a stub that marks the incident as "investigating"
                // and logs it. The full agent pipeline (investigator → evidence →
                // analyst → fix_proposer) will be implemented in the next commit.
                log.Printf("[investigation] incident %s created — agent pipeline pending implementation", incident.IncidentID)

                // Patch the CR status
                _ = k8sClient.PatchCRStatus(ctx, crName, crNamespace, "investigating")
        }

        // 6. Start the CRD watcher
        w, err := watcher.New(k8sClient, handler)
        if err != nil {
                log.Fatalf("[main] failed to init watcher: %v", err)
        }

        ctx, cancel := context.WithCancel(context.Background())
        defer cancel()

        go func() {
                log.Printf("[main] starting CRD watcher")
                w.Run(ctx)
        }()

        // Mark ready after a brief startup delay
        go func() {
                time.Sleep(2 * time.Second)
                ready = true
                log.Printf("[main] Sentinel ready — watching for incidents")
        }()

        // 7. Start the HTTP API
        apiServer := api.New(
                listenAddr,
                &storeAdapter{store: store},
                func() map[string]any {
                        return map[string]any{
                                "llm_provider":       cfg.LLMProvider,
                                "llm_fast_model":     cfg.LLMFastModel,
                                "llm_frontier_model": cfg.LLMFrontierModel,
                                "github_token_set":   cfg.GitHubToken != "",
                                "write_namespaces":   cfg.WriteNamespaces,
                        }
                },
                func(service, namespace, reason string) (string, error) {
                        cluster := schemas.AnomalyCluster{
                                Service:   service,
                                Namespace: namespace,
                                Severity:  schemas.SeverityWarning,
                                Summary:   fmt.Sprintf("Manual investigation: %s", reason),
                        }
                        handler(ctx, cluster, "manual", "sentinel")
                        return cluster.ClusterID.String(), nil
                },
                func() bool { return ready },
        )

        // 8. Wait for shutdown signal
        sigCh := make(chan os.Signal, 1)
        signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

        go func() {
                if err := apiServer.Start(); err != nil && err != http.ErrServerClosed {
                        log.Printf("[main] API server error: %v", err)
                }
        }()

        <-sigCh
        log.Printf("[main] shutting down...")
        cancel()

        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer shutdownCancel()
        _ = apiServer.Shutdown(shutdownCtx)

        log.Printf("[main] stopped")
}

// storeAdapter bridges feedback.Store to the api.Store interface.
// v3.0: returns []any from the typed []*schemas.Incident.
type storeAdapter struct {
        store *feedback.Store
}

func (a *storeAdapter) ListRecentIncidents(ctx context.Context, limit int) ([]any, error) {
        incidents, err := a.store.ListRecentIncidents(ctx, limit)
        if err != nil {
                return nil, err
        }
        out := make([]any, len(incidents))
        for i, inc := range incidents {
                out[i] = inc
        }
        return out, nil
}
