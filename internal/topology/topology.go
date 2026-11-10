// Package topology maps which workloads depend on which. Edges come from
// configuration now; logs, traces and observed traffic add their own later,
// each with a reliability, fused by noisy-OR.
package topology

import (
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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

// LogReliability is the chance that a Service an error line names is one
// the workload calls: errors name what failed to answer, and sometimes a
// name only in passing.
const LogReliability = 0.7

// ErrorHosts are the host names error lines name as addresses: a host and
// port, a URL, a name after "dial tcp" or "connect to", as clients print
// the connection they could not make. Masked values name nothing.
func ErrorHosts(lines []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lines {
		for _, word := range strings.Fields(l) {
			word = strings.Trim(word, `"'(),;[]{}:`)
			// Only an address names a host: a URL, or a host and its port.
			if strings.Contains(word, "<") || !strings.Contains(word, "://") && !hostPort.MatchString(word) {
				continue
			}
			for _, h := range hosts(word) {
				if h != "" && validHost(h) && !seen[h] && net.ParseIP(h) == nil {
					seen[h] = true
					out = append(out, h)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

var hostPort = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*:\d{1,5}$`)

// HostEdges are a workload's edges to the Services of its namespace that
// hosts name, learned from source with reliability.
func HostEdges(from model.ObjectRef, hostNames []string, services map[string]ServiceTarget, source string, reliability float64) []Edge {
	var out []Edge
	seen := map[string]bool{}
	for _, host := range hostNames {
		name := serviceName(host, from.Namespace)
		svc, ok := services[name]
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		for _, to := range svc.Backing {
			if to != from {
				out = append(out, Edge{From: from, To: to, Service: name, Source: source, Reliability: reliability})
			}
		}
	}
	return out
}

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

// ExternalHosts returns the hosts a pod template's literal configuration
// names that are not among services, Services of namespace ns: values that
// are plainly addresses (with a scheme, a port, or a domain), sorted.
func ExternalHosts(template corev1.PodTemplateSpec, ns string, services map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	consider := func(value string) {
		for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			host, ok := address(item)
			if !ok || seen[host] {
				continue
			}
			if name := serviceName(host, ns); name != "" && services[name] {
				continue
			}
			seen[host] = true
			out = append(out, host)
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
	sort.Strings(out)
	return out
}

// address returns the host of an item that is plainly a network address.
func address(item string) (string, bool) {
	if strings.Contains(item, "://") {
		u, err := url.Parse(item)
		if err != nil || u.Hostname() == "" {
			return "", false
		}
		return strings.ToLower(u.Hostname()), validHost(strings.ToLower(u.Hostname()))
	}
	host := item
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	if strings.ContainsAny(host, "/") {
		return "", false
	}
	h, port, err := net.SplitHostPort(host)
	switch {
	case err == nil:
		if _, perr := strconv.ParseUint(port, 10, 16); perr != nil {
			return "", false
		}
		host = h
	case !strings.Contains(host, "."):
		return "", false
	}
	host = strings.ToLower(host)
	return host, validHost(host)
}

// validHost accepts an IP address, or a DNS name whose last label is not
// all digits, which rules out version numbers.
func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || strings.Trim(l, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
	}
	return strings.Trim(labels[len(labels)-1], "0123456789") != ""
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

// Reach is a workload reached through the map, and how many edges away.
type Reach struct {
	Workload model.ObjectRef `json:"workload"`
	Hops     int             `json:"hops"`
}

// Dependents returns the workloads that depend on ref, directly or through
// others, nearest first.
func (m *Map) Dependents(ref model.ObjectRef) []model.ObjectRef {
	return workloads(m.walk(ref, func(e Edge) (model.ObjectRef, model.ObjectRef) { return e.To, e.From }))
}

// Dependencies returns the workloads ref depends on, directly or through
// others, nearest first.
func (m *Map) Dependencies(ref model.ObjectRef) []model.ObjectRef {
	return workloads(m.DependenciesWithHops(ref))
}

// DependenciesWithHops is Dependencies with each workload's distance.
func (m *Map) DependenciesWithHops(ref model.ObjectRef) []Reach {
	return m.walk(ref, func(e Edge) (model.ObjectRef, model.ObjectRef) { return e.From, e.To })
}

func workloads(rs []Reach) []model.ObjectRef {
	out := make([]model.ObjectRef, len(rs))
	for i, r := range rs {
		out[i] = r.Workload
	}
	return out
}

func (m *Map) walk(start model.ObjectRef, dir func(Edge) (model.ObjectRef, model.ObjectRef)) []Reach {
	seen := map[model.ObjectRef]bool{start: true}
	var out []Reach
	frontier := []model.ObjectRef{start}
	for hops := 1; len(frontier) > 0; hops++ {
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
		for _, w := range next {
			out = append(out, Reach{Workload: w, Hops: hops})
		}
		frontier = next
	}
	return out
}
