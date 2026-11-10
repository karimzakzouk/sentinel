// Package capture keeps what traffic capture's agent, OpenTelemetry's eBPF
// instrumentation (OBI) on each node, saw of the requests watched workloads
// served and made: method, route, status and timing, never a header. Bodies
// stay out unless the namespace's SentinelPolicy also sets capture.bodies.
// It is optional: without the agent nothing arrives, and the Lab works as
// before on faults that need no traffic. A namespace turns it on in its
// SentinelPolicy; requests of others are dropped as they arrive.
package capture

import (
	"cmp"
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
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	// Status is the HTTP status, or for a gRPC call, whose Method is
	// "gRPC", the one its code maps to; GRPC names the code.
	Status   int           `json:"status,omitempty"`
	GRPC     string        `json:"grpc,omitempty"`
	Duration time.Duration `json:"duration"`
	At       time.Time     `json:"at"`
	// Body and ResponseBody are kept only when the namespace opts in to
	// capture.bodies: redacted and capped. Empty by default.
	Body         string `json:"body,omitempty"`
	ResponseBody string `json:"responseBody,omitempty"`
}

// keep is how many requests are kept per workload, newest last.
const keep = 512

// bodyMax caps each stored body at about 4KiB.
const bodyMax = 4 << 10

// Store keeps the latest requests of each workload.
type Store struct {
	// Enabled says whether a namespace turned capture on.
	Enabled func(namespace string) bool
	// EnabledBodies says whether a namespace also keeps request and
	// response bodies. Nil or false means bodies are never stored.
	EnabledBodies func(namespace string) bool

	mu       sync.Mutex
	requests map[string][]Request
	seen     time.Time
}

// Add keeps a request of a workload, if its namespace turned capture on.
func (s *Store) Add(workload model.ObjectRef, r Request) bool {
	if s.Enabled == nil || !s.Enabled(workload.Namespace) {
		return false
	}
	if s.EnabledBodies == nil || !s.EnabledBodies(workload.Namespace) {
		r.Body, r.ResponseBody = "", ""
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

// Read is a GET a workload served, which a Lab can send its copy again
// without changing anything.
type Read struct {
	Path string
	// Port it was served on; 0 when the agent did not say.
	Port int
}

// Replayable returns, from a workload's recent requests, the GETs it
// served that production answered without a server error, each once, most
// frequent first, at most n.
func Replayable(requests []Request, n int) []Read {
	count := map[Read]int{}
	for _, r := range requests {
		if r.Served && r.Method == http.MethodGet && r.Status < 500 && !r.Redacted && replayablePath(r.Path) {
			count[Read{Path: r.Path, Port: r.Port}]++
		}
	}
	out := make([]Read, 0, len(count))
	for k := range count {
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b Read) int {
		if count[a] != count[b] {
			return count[b] - count[a]
		}
		return cmp.Or(strings.Compare(a.Path, b.Path), a.Port-b.Port)
	})
	return out[:min(len(out), n)]
}

// Call is one distinct outbound call a workload made: where, how, and the
// status production typically got.
type Call struct {
	Host, Method, Path, Route string
	Port, Status              int
	// Count is how often this call was seen among the requests.
	Count int
}

type callKey struct {
	Host, Method, Path string
	Port               int
}

// Calls returns, from a workload's recent requests, the distinct outbound
// calls (HTTP and gRPC), most frequent first, at most n. Path prefers the
// route template when the agent knew it. Status is the one seen most often
// for that host, port, method and path; a tie keeps the last. Redacted
// paths and calls with no host are skipped.
func Calls(requests []Request, n int) []Call {
	type agg struct {
		call     Call
		statuses map[int]int
		last     int
	}
	by := map[callKey]*agg{}
	for _, r := range requests {
		if r.Served || r.Redacted || r.Host == "" || r.Method == "" {
			continue
		}
		path := cmp.Or(r.Route, r.Path)
		if path == "" {
			continue
		}
		k := callKey{Host: strings.ToLower(r.Host), Port: r.Port, Method: r.Method, Path: path}
		a := by[k]
		if a == nil {
			a = &agg{call: Call{Host: k.Host, Method: k.Method, Path: k.Path, Route: r.Route, Port: k.Port}, statuses: map[int]int{}}
			by[k] = a
		}
		a.call.Count++
		a.statuses[r.Status]++
		a.last = r.Status
		if r.Route != "" {
			a.call.Route = r.Route
		}
	}
	out := make([]Call, 0, len(by))
	for _, a := range by {
		best, nBest := a.last, 0
		for status, c := range a.statuses {
			if c > nBest || c == nBest && status == a.last {
				best, nBest = status, c
			}
		}
		a.call.Status = best
		out = append(out, a.call)
	}
	slices.SortFunc(out, func(a, b Call) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return cmp.Or(
			strings.Compare(a.Host, b.Host),
			a.Port-b.Port,
			strings.Compare(a.Method, b.Method),
			strings.Compare(a.Path, b.Path),
		)
	})
	return out[:min(len(out), n)]
}

// BodySample is one non-GET request that carried a body. Lab seeding and
// write replay will read these (and a BodyStore keyed by request id when
// one exists); this helper is the hook stub for that path.
type BodySample struct {
	Method, Path, Host, Route string
	Port, Status              int
	Body, ResponseBody        string
}

// Bodies returns, from a workload's recent requests, non-GET samples that
// have a Body or ResponseBody, oldest first, at most n.
func Bodies(requests []Request, n int) []BodySample {
	var out []BodySample
	for _, r := range requests {
		if n >= 0 && len(out) >= n {
			break
		}
		if r.Method == "" || r.Method == http.MethodGet || r.Method == http.MethodHead {
			continue
		}
		if r.Body == "" && r.ResponseBody == "" {
			continue
		}
		out = append(out, BodySample{
			Method: r.Method, Path: r.Path, Host: r.Host, Route: r.Route,
			Port: r.Port, Status: r.Status, Body: r.Body, ResponseBody: r.ResponseBody,
		})
	}
	return out
}

// replayablePath is a path a Lab can send as it is: absolute, no longer
// than an Experiment allows, without a fragment or a space.
func replayablePath(p string) bool {
	return strings.HasPrefix(p, "/") && len(p) <= 1024 && !strings.ContainsAny(p, "?# \t\r\n")
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
		bodies := rc.Store.EnabledBodies != nil && rc.Store.EnabledBodies(workload.Namespace)
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				q, ok := requestOf(sp)
				if !ok {
					continue
				}
				if bodies {
					attachBodies(&q, attrs(sp.Attributes))
				}
				if rc.Store.Add(workload, q) {
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

// attrs reads attributes as strings. One with no value is kept empty: a
// zero, which protobuf's JSON leaves out.
func attrs(list []attribute) map[string]string {
	out := map[string]string{}
	for _, a := range list {
		switch {
		case a.Value.String != nil:
			out[a.Key] = *a.Value.String
		case a.Value.Int != "":
			out[a.Key] = a.Value.Int.String()
		default:
			out[a.Key] = ""
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

// requestOf reads an HTTP or gRPC span as a request. Other spans, such as
// a datastore's, whose statements carry data, are not kept.
func requestOf(sp span) (Request, bool) {
	a := attrs(sp.Attributes)
	if sp.Kind != kindServer && sp.Kind != kindClient {
		return Request{}, false
	}
	if cmp.Or(a["rpc.system.name"], a["rpc.system"]) == "grpc" {
		return grpcOf(sp, a)
	}
	method := a["http.request.method"]
	if method == "" {
		return Request{}, false
	}
	q := Request{Served: sp.Kind == kindServer, Method: method}
	q.Status, _ = strconv.Atoi(a["http.response.status_code"])
	timed(&q, sp)
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
	return addressed(q, a, path)
}

// grpcOf reads a gRPC span, under the semantic conventions before and
// after rpc.system became rpc.system.name.
func grpcOf(sp span, a map[string]string) (Request, bool) {
	q := Request{Served: sp.Kind == kindServer, Method: "gRPC"}
	timed(&q, sp)
	method := cmp.Or(a["rpc.method"], sp.Name)
	if service := a["rpc.service"]; service != "" && !strings.Contains(method, "/") {
		method = service + "/" + method
	}
	if method == "" {
		return Request{}, false
	}
	status, found := a["rpc.status_code"]
	if !found {
		status, found = a["rpc.grpc.status_code"]
	}
	if code, ok := grpcCode(cmp.Or(status, "0")); found && ok {
		q.GRPC, q.Status = grpcNames[code], grpcHTTP[code]
	}
	return addressed(q, a, "/"+strings.TrimPrefix(method, "/"))
}

func timed(q *Request, sp span) {
	start, _ := strconv.ParseInt(sp.Start, 10, 64)
	end, _ := strconv.ParseInt(sp.End, 10, 64)
	q.At = time.Unix(0, start).UTC()
	if end > start {
		q.Duration = time.Duration(end - start)
	}
}

// addressed adds where a request went and its cleaned path.
func addressed(q Request, a map[string]string, path string) (Request, bool) {
	if h := a["server.address"]; h != "" && !q.Served {
		q.Host = h
	}
	if port, err := strconv.Atoi(a["server.port"]); err == nil && port > 0 && port < 1<<16 {
		q.Port = port
	}
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

// gRPC's status codes, by number: their names, and the HTTP statuses they
// map to, as gRPC's HTTP gateway maps them.
var (
	grpcNames = []string{"OK", "CANCELLED", "UNKNOWN", "INVALID_ARGUMENT", "DEADLINE_EXCEEDED", "NOT_FOUND", "ALREADY_EXISTS",
		"PERMISSION_DENIED", "RESOURCE_EXHAUSTED", "FAILED_PRECONDITION", "ABORTED", "OUT_OF_RANGE", "UNIMPLEMENTED", "INTERNAL",
		"UNAVAILABLE", "DATA_LOSS", "UNAUTHENTICATED"}
	grpcHTTP = []int{200, 499, 500, 400, 504, 404, 409, 403, 429, 400, 409, 400, 501, 500, 503, 500, 401}
)

// grpcCode reads a gRPC status, by number or by name.
func grpcCode(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, n >= 0 && n < len(grpcNames)
	}
	name := strings.ToUpper(s)
	if name == "CANCELED" {
		name = "CANCELLED"
	}
	i := slices.Index(grpcNames, name)
	return i, i >= 0
}

// attachBodies copies body attributes from a span when present, capped and
// redacted. Known keys are the plain and ".content" forms an exporter may
// send; absent attributes leave the fields empty.
func attachBodies(q *Request, a map[string]string) {
	q.Body = clippedBody(cmp.Or(a["http.request.body.content"], a["http.request.body"]))
	q.ResponseBody = clippedBody(cmp.Or(a["http.response.body.content"], a["http.response.body"]))
}

func clippedBody(s string) string {
	if s == "" {
		return ""
	}
	clean, _ := redactor.String(s)
	if len(clean) > bodyMax {
		return clean[:bodyMax]
	}
	return clean
}

var redactor = redact.New()
