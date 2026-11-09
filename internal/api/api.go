// Package api serves Sentinel's read-only HTTP API and its UI. It listens on
// localhost inside Sentinel's pod, so the only way in is kubectl
// port-forward, which Kubernetes RBAC already guards. Everything it serves
// comes from the Ledger, which holds only redacted content.
package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/capability"
	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/stats"
)

// Server answers the UI's questions from the Ledger.
type Server struct {
	Ledger  *ledger.Ledger
	Version string
	// Verified is the result of the Ledger check the controller ran at start.
	Verified Verification
	// UI serves the web app; nil serves the API alone.
	UI http.Handler
	// Acting, when set, reports whether a policy lets Sentinel run actions
	// on its own somewhere.
	Acting func() bool
}

// Verification is a completed check of the Ledger's hash chain.
type Verification struct {
	Events int64     `json:"events"`
	At     time.Time `json:"at"`
}

// maxIncidents bounds the incident list.
const maxIncidents = 500

// Handler returns the API and UI routes, behind the host check and security
// headers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/summary", s.summary)
	mux.HandleFunc("GET /api/incidents", s.incidents)
	mux.HandleFunc("GET /api/incidents/{id}", s.incident)
	mux.HandleFunc("GET /api/capabilities", s.capabilities)
	mux.HandleFunc("GET /api/actions", s.actions)
	mux.HandleFunc("GET /api/trackrecords", s.trackRecords)
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, errors.New("no such API route"))
	})
	if s.UI != nil {
		mux.Handle("GET /", s.UI)
	}
	return localOnly(secure(mux))
}

// Summary is the header of every page.
type Summary struct {
	Version string `json:"version"`
	// Mode is "observe" when Sentinel records what it would do and changes
	// nothing, and "assist" when a namespace's policy lets it run reversible
	// actions on its own.
	Mode   string `json:"mode"`
	Ledger struct {
		HeadSeq  int64        `json:"headSeq"`
		HeadHash string       `json:"headHash"`
		Verified Verification `json:"verified"`
	} `json:"ledger"`
	Incidents struct {
		Open  int `json:"open"`
		Total int `json:"total"`
	} `json:"incidents"`
	Cluster  *capability.Cluster  `json:"cluster,omitempty"`
	Features []capability.Feature `json:"features,omitempty"`
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := Summary{Version: s.Version, Mode: "observe"}
	if s.Acting != nil && s.Acting() {
		out.Mode = "assist"
	}
	seq, hash, err := s.Ledger.Head(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out.Ledger.HeadSeq, out.Ledger.HeadHash, out.Ledger.Verified = seq, hash, s.Verified
	incidents, err := ledger.List[model.Incident](ctx, s.Ledger, model.KindIncident, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, inc := range incidents {
		if inc.MergedInto != "" {
			continue
		}
		out.Incidents.Total++
		if open(inc) {
			out.Incidents.Open++
		}
	}
	if rep, ok, err := s.latestCapabilities(r); err == nil && ok {
		out.Cluster, out.Features = &rep.Cluster, rep.Features
	}
	writeJSON(w, out)
}

func open(inc model.Incident) bool {
	return inc.State != model.IncidentResolved && inc.State != model.IncidentClosed
}

func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	incidents, err := ledger.List[model.Incident](r.Context(), s.Ledger, model.KindIncident, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	sort.SliceStable(incidents, func(i, j int) bool {
		if oi, oj := open(incidents[i]), open(incidents[j]); oi != oj {
			return oi
		}
		return incidents[i].OpenedAt.After(incidents[j].OpenedAt)
	})
	if len(incidents) > maxIncidents {
		incidents = incidents[:maxIncidents]
	}
	writeJSON(w, map[string]any{"incidents": incidents})
}

// ActionSummary is an action with its first and latest recorded steps.
type ActionSummary struct {
	Action model.Action `json:"action"`
	// Stage is the latest step, such as requested, executed or verified.
	Stage string    `json:"stage"`
	Since time.Time `json:"since"`
	At    time.Time `json:"at"`
}

// maxActionEvents bounds the action steps the actions list reads.
const maxActionEvents = 5000

// actions lists every action Sentinel proposed or took, newest first.
func (s *Server) actions(w http.ResponseWriter, r *http.Request) {
	events, err := s.Ledger.LatestOfTypes(r.Context(), "sentinel.action.", maxActionEvents)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var out []ActionSummary
	index := map[model.ID]int{}
	for _, e := range events {
		i, seen := index[e.EntityID]
		if !seen {
			var a model.Action
			if err := json.Unmarshal(e.Data, &a); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			index[e.EntityID] = len(out)
			out = append(out, ActionSummary{Action: a, Stage: strings.TrimPrefix(e.Type, "sentinel.action."), Since: e.Time, At: e.Time})
			continue
		}
		out[i].Since = e.Time
	}
	writeJSON(w, map[string]any{"actions": out})
}

// TrackSummary is one cell's track record, with what it says.
type TrackSummary struct {
	model.TrackRecord
	// Successes and Failures are the verified actions and those that did not
	// clear their incident; the record's prior adds one of each.
	Successes int `json:"successes"`
	Failures  int `json:"failures"`
	// Lower is the 5th percentile of the success rate.
	Lower float64 `json:"lower"`
}

// trackRecords lists every cell's track record, the most used first.
func (s *Server) trackRecords(w http.ResponseWriter, r *http.Request) {
	records, err := ledger.List[model.TrackRecord](r.Context(), s.Ledger, model.KindTrackRecord, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]TrackSummary, len(records))
	for i, t := range records {
		out[i] = TrackSummary{TrackRecord: t, Successes: int(t.Alpha) - 1, Failures: int(t.Beta) - 1,
			Lower: stats.BetaQuantile(0.05, t.Alpha, t.Beta)}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Alpha+out[i].Beta > out[j].Alpha+out[j].Beta })
	writeJSON(w, map[string]any{"trackRecords": out})
}

// IncidentDetail is an incident and everything recorded about it, in order.
type IncidentDetail struct {
	Incident model.Incident `json:"incident"`
	Events   []Event        `json:"events"`
}

// Event is a Ledger event as the UI shows it.
type Event struct {
	Seq  int64           `json:"seq"`
	Time time.Time       `json:"time"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func (s *Server) incident(w http.ResponseWriter, r *http.Request) {
	id := model.ID(r.PathValue("id"))
	var out IncidentDetail
	found, err := s.Ledger.Get(r.Context(), model.KindIncident, id, &out.Incident)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
		return
	case !found:
		writeError(w, http.StatusNotFound, errors.New("no such incident"))
		return
	}
	events, err := s.Ledger.IncidentEvents(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, e := range events {
		out.Events = append(out.Events, Event{Seq: e.Seq, Time: e.Time, Type: e.Type, Data: e.Data})
	}
	writeJSON(w, out)
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	rep, ok, err := s.latestCapabilities(r)
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
	case !ok:
		writeError(w, http.StatusNotFound, errors.New("no capability report yet"))
	default:
		writeJSON(w, rep)
	}
}

func (s *Server) latestCapabilities(r *http.Request) (capability.Report, bool, error) {
	var rep capability.Report
	events, err := s.Ledger.Latest(r.Context(), "sentinel.capability.reported", 1)
	if err != nil || len(events) == 0 {
		return rep, false, err
	}
	return rep, true, json.Unmarshal(events[0].Data, &rep)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// localOnly refuses requests whose Host is not a loopback name. A web page
// the operator opens elsewhere could otherwise point a name it controls at
// 127.0.0.1 and read the port-forwarded API (DNS rebinding).
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopback(r.Host) {
			http.Error(w, "Sentinel's UI answers only on localhost, through kubectl port-forward", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loopback(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
