// Package api provides the HTTP API for Sentinel v3.
// Replaces the FastAPI app in v2 + the Next.js UI pod.
// Routes:
//   GET  /healthz        — liveness
//   GET  /readyz         — readiness (DB + watcher running)
//   GET  /metrics        — Prometheus metrics
//   GET  /incidents      — list recent incidents
//   GET  /incidents/{id} — single incident
//   GET  /metrics-summary — dashboard summary
//   GET  /settings       — runtime settings (masked)
//   POST /settings       — update settings
//   POST /admin/investigate — manually trigger an investigation
package api

import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "net/http"
        "strings"

        "github.com/google/uuid"
)

// Server is the HTTP API server.
type Server struct {
        store        Store
        settings     SettingsGetter
        investigate  InvestigateFunc
        readyChecker ReadyChecker
        http         *http.Server
}

// Store interface — satisfied by feedback.Store.
type Store interface {
        ListRecentIncidents(ctx context.Context, limit int) ([]any, error)
}

// SettingsGetter returns current runtime settings (masked).
type SettingsGetter func() map[string]any

// InvestigateFunc manually triggers an investigation.
type InvestigateFunc func(service, namespace, reason string) (string, error)

// ReadyChecker returns true when the system is ready to serve.
type ReadyChecker func() bool

// New creates a new API server.
func New(addr string, store Store, settings SettingsGetter, investigate InvestigateFunc, ready ReadyChecker) *Server {
        s := &Server{
                store:        store,
                settings:     settings,
                investigate:  investigate,
                readyChecker: ready,
        }
        mux := http.NewServeMux()
        mux.HandleFunc("/healthz", s.handleHealthz)
        mux.HandleFunc("/readyz", s.handleReadyz)
        mux.HandleFunc("/metrics", s.handleMetrics)
        mux.HandleFunc("/metrics-summary", s.handleMetricsSummary)
        mux.HandleFunc("/incidents", s.handleIncidents)
        mux.HandleFunc("/incidents/", s.handleIncident)
        mux.HandleFunc("/settings", s.handleSettings)
        mux.HandleFunc("/admin/investigate", s.handleInvestigate)

        s.http = &http.Server{
                Addr:    addr,
                Handler: mux,
        }
        return s
}

// Start begins serving HTTP.
func (s *Server) Start() error {
        log.Printf("[api] listening on %s", s.http.Addr)
        return s.http.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
        return s.http.Shutdown(ctx)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
        writeJSON(w, 200, map[string]string{
                "status":  "ok",
                "service": "core",
                "version": "3.2.3",
        })
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
        ready := s.readyChecker()
        status := "not_ready"
        if ready {
                status = "ready"
        }
        writeJSON(w, 200, map[string]any{
                "status": status,
                "ready":  ready,
        })
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain; version=0.0.4")
        // Minimal Prometheus metrics — v3.1 will add more
        fmt.Fprintf(w, "# HELP sentinel_incidents_total Total incidents processed\n")
        fmt.Fprintf(w, "# TYPE sentinel_incidents_total counter\n")
        fmt.Fprintf(w, "sentinel_incidents_total 0\n")
}

func (s *Server) handleMetricsSummary(w http.ResponseWriter, r *http.Request) {
        incidents, _ := s.store.ListRecentIncidents(r.Context(), 100)
        active, resolved, failed := 0, 0, 0
        for _, inc := range incidents {
                m, ok := inc.(map[string]any)
                if !ok {
                        continue
                }
                status, _ := m["status"].(string)
                switch {
                case strings.Contains("investigating root_caused fix_proposed approved executing", status):
                        active++
                case strings.Contains("verified resolved", status):
                        resolved++
                case strings.Contains("failed rejected escalated", status):
                        failed++
                }
        }
        writeJSON(w, 200, map[string]int{
                "active":       active,
                "resolved_24h": resolved,
                "failed":       failed,
                "total":        len(incidents),
        })
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
        limit := 50
        if l := r.URL.Query().Get("limit"); l != "" {
                if n, err := fmt.Sscanf(l, "%d", &limit); err != nil || n == 0 {
                        limit = 50
                }
        }
        incidents, err := s.store.ListRecentIncidents(r.Context(), limit)
        if err != nil {
                writeJSON(w, 500, map[string]string{"error": err.Error()})
                return
        }
        writeJSON(w, 200, incidents)
}

func (s *Server) handleIncident(w http.ResponseWriter, r *http.Request) {
        idStr := strings.TrimPrefix(r.URL.Path, "/incidents/")
        id, err := uuid.Parse(idStr)
        if err != nil {
                writeJSON(w, 400, map[string]string{"error": "invalid incident ID"})
                return
        }
        _ = id
        // v3.0: implement GetIncident in the store interface
        writeJSON(w, 404, map[string]string{"error": "not implemented yet"})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
        if r.Method == "GET" {
                writeJSON(w, 200, s.settings())
                return
        }
        // POST — update settings (v3.1 will implement this)
        writeJSON(w, 501, map[string]string{"error": "settings update not implemented yet"})
}

func (s *Server) handleInvestigate(w http.ResponseWriter, r *http.Request) {
        if r.Method != "POST" {
                writeJSON(w, 405, map[string]string{"error": "method not allowed"})
                return
        }
        var spec struct {
                Service   string `json:"service"`
                Namespace string `json:"namespace"`
                Reason    string `json:"reason"`
        }
        if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
                writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
                return
        }
        if spec.Service == "" {
                writeJSON(w, 400, map[string]string{"error": "service is required"})
                return
        }
        if spec.Namespace == "" {
                spec.Namespace = "default"
        }
        if spec.Reason == "" {
                spec.Reason = "manual trigger"
        }

        clusterID, err := s.investigate(spec.Service, spec.Namespace, spec.Reason)
        if err != nil {
                writeJSON(w, 500, map[string]string{"error": err.Error()})
                return
        }
        writeJSON(w, 200, map[string]string{
                "status":     "triggered",
                "service":    spec.Service,
                "cluster_id": clusterID,
        })
}

func writeJSON(w http.ResponseWriter, status int, v any) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        _ = json.NewEncoder(w).Encode(v)
}
