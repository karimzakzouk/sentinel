package nodeagent

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// keep is how many redacted lines are kept per pod/container, newest last.
const keep = 256

// Store keeps recent redacted lines the agent posted, keyed by
// namespace/pod/container. OnBatch, when set, is the hook the watcher (or
// anything else) uses to turn a batch into facts or evidence.
type Store struct {
	// Allowed, when set, drops lines whose namespace is not allowed.
	Allowed func(namespace string) bool
	// OnBatch is called after a batch is accepted. It must not block long.
	OnBatch func(Batch)

	mu    sync.Mutex
	lines map[string][]Line
	seen  time.Time
}

// Add keeps a line when its namespace is allowed.
func (s *Store) Add(line Line) bool {
	if s.Allowed != nil && !s.Allowed(line.Namespace) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lines == nil {
		s.lines = map[string][]Line{}
	}
	key := line.Namespace + "/" + line.Pod + "/" + line.Container
	list := append(s.lines[key], line)
	s.lines[key] = list[max(0, len(list)-keep):]
	if line.At.After(s.seen) {
		s.seen = line.At
	}
	return true
}

// Recent returns a container's lines since a time, oldest first.
func (s *Store) Recent(namespace, pod, container string, since time.Time) []Line {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Line
	for _, l := range s.lines[namespace+"/"+pod+"/"+container] {
		if !l.At.Before(since) {
			out = append(out, l)
		}
	}
	return out
}

// LastSeen is when the newest line arrived, zero when none has.
func (s *Store) LastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen
}

// Receiver accepts JSON batches from the node agent.
type Receiver struct {
	Store *Store
	Token string
	Log   *slog.Logger
}

func (rc *Receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/logs" {
		http.NotFound(w, r)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if rc.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(rc.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "send JSON", http.StatusUnsupportedMediaType)
		return
	}
	var batch Batch
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&batch); err != nil {
		http.Error(w, "bad JSON", http.StatusBadRequest)
		return
	}
	accepted := Batch{Node: batch.Node}
	for _, line := range batch.Lines {
		if line.Namespace == "" || line.Pod == "" || line.Container == "" || line.Text == "" {
			continue
		}
		if line.At.IsZero() {
			line.At = time.Now().UTC()
		}
		if rc.Store != nil && !rc.Store.Add(line) {
			continue
		}
		accepted.Lines = append(accepted.Lines, line)
	}
	for _, c := range batch.Cgroups {
		if c.Namespace == "" || c.Pod == "" {
			continue
		}
		if rc.Store != nil && rc.Store.Allowed != nil && !rc.Store.Allowed(c.Namespace) {
			continue
		}
		accepted.Cgroups = append(accepted.Cgroups, c)
	}
	if rc.Store != nil && rc.Store.OnBatch != nil && (len(accepted.Lines) > 0 || len(accepted.Cgroups) > 0) {
		rc.Store.OnBatch(accepted)
	}
	if rc.Log != nil && (len(accepted.Lines) > 0 || len(accepted.Cgroups) > 0) {
		rc.Log.Debug("node agent batch", "node", batch.Node, "lines", len(accepted.Lines), "cgroups", len(accepted.Cgroups))
	}
	w.WriteHeader(http.StatusOK)
}
