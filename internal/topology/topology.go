// Package topology maps which workloads depend on which. Edges come from
// configuration now; logs, traces and observed traffic add their own later,
// each with a reliability, fused by noisy-OR.
package topology

import (
	"net/url"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/karimzakzouk/sentinel/internal/model"
)

// Edge says a workload calls a Service, and through it the workloads behind it.
type Edge struct {
	From model.ObjectRef `json:"from"`
	To   model.ObjectRef `json:"to"`
	// Service is the Service the call goes through.
	Service string `json:"service"`
	// Source is where the edge was learned, such as "config".
	Source string `json:"source"`
	// Reliability is the chance an edge from this source is real.
	Reliability float64 `json:"reliability"`
}

// ConfigReliability is the chance that a Service named in a workload's
// configuration is one it calls. Named but unused addresses happen.
const ConfigReliability = 0.9

// ServiceTarget is a Service and the workloads behind it.
type ServiceTarget struct {
	Name    string
	Backing []model.ObjectRef
}

// ConfigEdges derives a workload's edges from its pod template: literal
// environment values and arguments that name a Service in its namespace,
// as a host, host:port, URL or cluster DNS name. Values from ConfigMaps and
// Secrets are not read.
func ConfigEdges(from model.ObjectRef, template corev1.PodTemplateSpec, services map[string]ServiceTarget) []Edge {
	seen := map[string]bool{}
	var out []Edge
	consider := func(value string) {
		for _, host := range hosts(value) {
			name := serviceName(host, from.Namespace)
			svc, ok := services[name]
			if !ok || seen[name] {
				continue
			}
			seen[name] = true
			targets := svc.Backing
			if len(targets) == 0 {
				targets = []model.ObjectRef{{Kind: "Service", Namespace: from.Namespace, Name: name}}
			}
			for _, to := range targets {
				if to != from {
					out = append(out, Edge{From: from, To: to, Service: name, Source: "config", Reliability: ConfigReliability})
				}
			}
		}
	}
	for _, c := range append(template.Spec.InitContainers, template.Spec.Containers...) {
		for _, e := range c.Env {
			consider(e.Value)
		}
		for _, a := range append(c.Command, c.Args...) {
			if _, v, ok := strings.Cut(a, "="); ok {
				consider(v)
			} else {
				consider(a)
			}
		}
	}
	return out
}

// hosts returns the host names a configuration value may hold: one per
// comma-, semicolon- or space-separated item, with schemes, credentials,
// ports and paths removed.
func hosts(value string) []string {
	var out []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if strings.Contains(item, "://") {
			if u, err := url.Parse(item); err == nil && u.Hostname() != "" {
				out = append(out, u.Hostname())
			}
			continue
		}
		host := item
		if i := strings.LastIndexByte(host, '@'); i >= 0 {
			host = host[i+1:]
		}
		if i := strings.IndexAny(host, ":/"); i >= 0 {
			host = host[:i]
		}
		if host != "" {
			out = append(out, strings.ToLower(host))
		}
	}
	return out
}

// serviceName returns the Service a host names in namespace ns: the host
// itself, or a cluster DNS name for the namespace (name.ns, name.ns.svc,
// name.ns.svc.<cluster domain>). Anything else returns "".
func serviceName(host, ns string) string {
	name, rest, dotted := strings.Cut(host, ".")
	if !dotted {
		return name
	}
	if rest == ns || strings.HasPrefix(rest, ns+".svc") && (rest == ns+".svc" || strings.HasPrefix(rest, ns+".svc.")) {
		return name
	}
	return ""
}

// Map is a dependency graph.
type Map struct {
	edges []Edge
}

// NewMap builds a map from edges.
func NewMap(edges []Edge) *Map { return &Map{edges: edges} }

// Edges returns every edge.
func (m *Map) Edges() []Edge { return m.edges }

// Dependents returns the workloads that depend on ref, directly or through
// others, nearest first.
func (m *Map) Dependents(ref model.ObjectRef) []model.ObjectRef {
	return m.walk(ref, func(e Edge) (model.ObjectRef, model.ObjectRef) { return e.To, e.From })
}

// Dependencies returns the workloads ref depends on, directly or through
// others, nearest first.
func (m *Map) Dependencies(ref model.ObjectRef) []model.ObjectRef {
	return m.walk(ref, func(e Edge) (model.ObjectRef, model.ObjectRef) { return e.From, e.To })
}

func (m *Map) walk(start model.ObjectRef, dir func(Edge) (model.ObjectRef, model.ObjectRef)) []model.ObjectRef {
	seen := map[model.ObjectRef]bool{start: true}
	var out []model.ObjectRef
	frontier := []model.ObjectRef{start}
	for len(frontier) > 0 {
		var next []model.ObjectRef
		for _, cur := range frontier {
			for _, e := range m.edges {
				if from, to := dir(e); from == cur && !seen[to] {
					seen[to] = true
					next = append(next, to)
				}
			}
		}
		sort.Slice(next, func(i, j int) bool { return next[i].String() < next[j].String() })
		out = append(out, next...)
		frontier = next
	}
	return out
}
