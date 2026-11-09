// Package capture keeps what traffic capture's agent, OpenTelemetry's eBPF
// instrumentation (OBI) on each node, saw of the requests watched workloads
// served and made: method, route, status and timing, never a body or a
// header. It is optional: without the agent nothing arrives, and the Lab
// works as before on faults that need no traffic. A namespace turns it on
// in its SentinelPolicy; requests of others are dropped as they arrive.
package capture

import (
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/redact"
)

// Request is one request a workload served, or one call it made.
type Request struct {
	// Served is true for a request the workload answered, false for a
	// call it made to something else.
	Served bool   `json:"served"`
	Method string `json:"method"`
	// Path is the path, with anything that looks like a secret redacted
	// and the query string dropped; Route is its template, when the agent
	// knew it, such as /cart/{id}. Redacted marks a path a secret was taken
	// out of, which is not replayed.
	Path     string `json:"path"`
	Route    string `json:"route,omitempty"`
	Redacted bool   `json:"redacted,omitempty"`
	// Host and Port are where a call went.
	Host     string        `json:"host,omitempty"`
	Port     int           `json:"port,omitempty"`
	Status   int           `json:"status,omitempty"`
	Duration time.Duration `json:"duration"`
	At       time.Time     `json:"at"`
}

// keep is how many requests are kept per workload, newest last.
const keep = 512

// Store keeps the latest requests of each workload.
type Store struct {
	// Enabled says whether a namespace turned capture on.
	Enabled func(namespace string) bool

	mu       sync.Mutex
	requests map[string][]Request
	seen     time.Time
}

// Add keeps a request of a workload, if its namespace turned capture on.
func (s *Store) Add(workload model.ObjectRef, r Request) bool {
	if s.Enabled == nil || !s.Enabled(workload.Namespace) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requests == nil {
		s.requests = map[string][]Request{}
	}
	key := workload.String()
	list := append(s.requests[key], r)
	s.requests[key] = list[max(0, len(list)-keep):]
	if r.At.After(s.seen) {
		s.seen = r.At
	}
	return true
}

// Recent returns a workload's requests since a time, oldest first.
func (s *Store) Recent(workload model.ObjectRef, since time.Time) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Request
	for _, r := range s.requests[workload.String()] {
		if !r.At.Before(since) {
			out = append(out, r)
		}
	}
	return out
}

// LastSeen is when the newest request arrived, zero when none has.
func (s *Store) LastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

// Replayable returns, from a workload's recent requests, the reads it
// served (GET and HEAD) that production answered without a server error,
// each method and path once, most frequent first, at most n: what a Lab can
// send its copy again without changing anything.
func Replayable(requests []Request, n int) []string {
	count := map[string]int{}
	for _, r := range requests {
		if r.Served && (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.Status < 500 && r.Path != "" && !r.Redacted {
			count[r.Method+" "+r.Path]++
		}
	}
	out := make([]string, 0, len(count))
	for k := range count {
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b string) int {
		if count[a] != count[b] {
			return count[b] - count[a]
		}
		return strings.Compare(a, b)
	})
	return out[:min(len(out), n)]
}

// Receiver accepts traces as OTLP over HTTP, in JSON, from the capture
// agent, which sends the bearer token it was given.
type Receiver struct {
	Store *Store
	Token string
	Log   *slog.Logger
}

func (rc *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" {
		http.NotFound(w, r)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if rc.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(rc.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "send OTLP as JSON", http.StatusUnsupportedMediaType)
		return
	}
	body := io.LimitReader(r.Body, 8<<20)
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(body)
		if err != nil {
			http.Error(w, "bad gzip", http.StatusBadRequest)
			return
		}
		defer func() { _ = zr.Close() }()
		body = io.LimitReader(zr, 32<<20)
	}
	var req exportRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		http.Error(w, "not OTLP JSON", http.StatusBadRequest)
		return
	}
	kept := 0
	for _, rs := range req.ResourceSpans {
		workload, ok := workloadOf(rs.Resource.Attributes)
		if !ok {
			continue
		}
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				if q, ok := requestOf(sp); ok && rc.Store.Add(workload, q) {
					kept++
				}
			}
		}
	}
	if rc.Log != nil && kept > 0 {
		rc.Log.Debug("captured requests", "kept", kept)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

// OTLP's JSON, as much of it as capture reads.
type exportRequest struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []attribute `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []span `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type span struct {
	Name       string      `json:"name"`
	Kind       int         `json:"kind"`
	Start      string      `json:"startTimeUnixNano"`
	End        string      `json:"endTimeUnixNano"`
	Attributes []attribute `json:"attributes"`
}

type attribute struct {
	Key   string `json:"key"`
	Value struct {
		String *string     `json:"stringValue"`
		Int    json.Number `json:"intValue"`
	} `json:"value"`
}

const (
	kindServer = 2
	kindClient = 3
)

func attrs(list []attribute) map[string]string {
	out := map[string]string{}
	for _, a := range list {
		switch {
		case a.Value.String != nil:
			out[a.Key] = *a.Value.String
		case a.Value.Int != "":
			out[a.Key] = a.Value.Int.String()
		}
	}
	return out
}

// workloadOf names the workload a resource's spans come from.
func workloadOf(list []attribute) (model.ObjectRef, bool) {
	a := attrs(list)
	ns := a["k8s.namespace.name"]
	for _, k := range []struct{ attr, kind string }{
		{"k8s.deployment.name", "Deployment"}, {"k8s.statefulset.name", "StatefulSet"}, {"k8s.daemonset.name", "DaemonSet"},
	} {
		if name := a[k.attr]; name != "" && ns != "" {
			return model.ObjectRef{Group: "apps", Kind: k.kind, Namespace: ns, Name: name}, true
		}
	}
	return model.ObjectRef{}, false
}

// requestOf reads an HTTP span as a request.
func requestOf(sp span) (Request, bool) {
	a := attrs(sp.Attributes)
	method := a["http.request.method"]
	if method == "" || sp.Kind != kindServer && sp.Kind != kindClient {
		return Request{}, false
	}
	q := Request{Served: sp.Kind == kindServer, Method: method}
	q.Status, _ = strconv.Atoi(a["http.response.status_code"])
	start, _ := strconv.ParseInt(sp.Start, 10, 64)
	end, _ := strconv.ParseInt(sp.End, 10, 64)
	q.At = time.Unix(0, start).UTC()
	if end > start {
		q.Duration = time.Duration(end - start)
	}
	q.Route = a["http.route"]
	path := a["url.path"]
	if full := a["url.full"]; full != "" {
		if u, err := url.Parse(full); err == nil {
			if path == "" {
				path = u.Path
			}
			q.Host = u.Hostname()
		}
	}
	if h := a["server.address"]; h != "" && !q.Served {
		q.Host = h
	}
	q.Port, _ = strconv.Atoi(a["server.port"])
	if !q.Served && q.Host == "" {
		return Request{}, false
	}
	if path == "" {
		path = q.Route
	}
	path, _, _ = strings.Cut(path, "?")
	clean, found := redactor.String(path)
	q.Path, q.Redacted = clean, len(found) > 0
	return q, true
}

var redactor = redact.New()
