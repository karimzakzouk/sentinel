// Package capability detects what Sentinel can see and do on a cluster and
// reports it honestly. Every feature is either enabled or disabled with a
// stated reason; a missing prerequisite never silently weakens safety.
package capability

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Status is the outcome of one check.
type Status string

const (
	Available   Status = "available"
	Unavailable Status = "unavailable"
	// Unknown means the check could not be decided, for example because an
	// admission webhook refused a dry run. It is never treated as available.
	Unknown Status = "unknown"
)

// Check is one observed fact about the cluster.
type Check struct {
	ID     string `json:"id"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Feature is a Sentinel capability derived from checks.
type Feature struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
}

// Node is the per-node coverage map.
type Node struct {
	Name             string   `json:"name"`
	KubeletVersion   string   `json:"kubeletVersion"`
	KernelVersion    string   `json:"kernelVersion"`
	ContainerRuntime string   `json:"containerRuntime"`
	OSImage          string   `json:"osImage"`
	Architecture     string   `json:"architecture"`
	Tiers            []string `json:"tiers"`
	Notes            []string `json:"notes,omitempty"`
}

// Cluster summarizes the cluster.
type Cluster struct {
	Platform      string `json:"platform"`
	ServerVersion string `json:"serverVersion"`
	Nodes         int    `json:"nodes"`
	NetworkPlugin string `json:"networkPlugin,omitempty"`
}

// Report is the full capability report.
type Report struct {
	GeneratedAt time.Time `json:"generatedAt"`
	// Identity is the user the checks ran as; permissions are reported for it.
	Identity string    `json:"identity"`
	Cluster  Cluster   `json:"cluster"`
	Features []Feature `json:"features"`
	Checks   []Check   `json:"checks"`
	Nodes    []Node    `json:"nodes"`
	// Warnings flag risks, such as Sentinel holding permissions it must not have.
	Warnings []string `json:"warnings,omitempty"`
}

// Check returns the check with the given ID.
func (r *Report) Check(id string) (Check, bool) {
	for _, c := range r.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return Check{}, false
}

// Feature returns the feature with the given name.
func (r *Report) Feature(name string) (Feature, bool) {
	for _, f := range r.Features {
		if f.Name == name {
			return f, true
		}
	}
	return Feature{}, false
}

func (r *Report) add(id string, s Status, format string, args ...any) {
	r.Checks = append(r.Checks, Check{ID: id, Status: s, Detail: fmt.Sprintf(format, args...)})
}

func (r *Report) feature(name string, enabled bool, format string, args ...any) {
	r.Features = append(r.Features, Feature{Name: name, Enabled: enabled, Reason: fmt.Sprintf(format, args...)})
}

func (r *Report) sortChecks() {
	sort.SliceStable(r.Checks, func(i, j int) bool { return r.Checks[i].ID < r.Checks[j].ID })
}

// WriteText renders the report for people.
func (r *Report) WriteText(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(tw, format, args...) }

	p("Sentinel capability report\n")
	p("Cluster:\t%s, Kubernetes %s, %d node(s)\n", r.Cluster.Platform, r.Cluster.ServerVersion, r.Cluster.Nodes)
	if r.Cluster.NetworkPlugin != "" {
		p("Network plugin:\t%s\n", r.Cluster.NetworkPlugin)
	}
	p("Checked as:\t%s\n", r.Identity)
	if !strings.Contains(r.Identity, "sentinel") {
		p("\t(permission findings describe this identity; use --as system:serviceaccount:<namespace>:sentinel to check Sentinel's own)\n")
	}
	p("Generated:\t%s\n\n", r.GeneratedAt.UTC().Format(time.RFC3339))

	p("FEATURE\tSTATE\tWHY\n")
	for _, f := range r.Features {
		state := "off"
		if f.Enabled {
			state = "on"
		}
		p("%s\t%s\t%s\n", f.Name, state, f.Reason)
	}
	p("\nCHECK\tSTATUS\tDETAIL\n")
	for _, c := range r.Checks {
		p("%s\t%s\t%s\n", c.ID, c.Status, c.Detail)
	}
	if len(r.Nodes) > 0 {
		p("\nNODE\tTIERS\tKERNEL\tRUNTIME\tNOTES\n")
		for _, n := range r.Nodes {
			p("%s\t%s\t%s\t%s\t%s\n", n.Name, strings.Join(n.Tiers, ","), n.KernelVersion, n.ContainerRuntime, strings.Join(n.Notes, "; "))
		}
	}
	if len(r.Warnings) > 0 {
		p("\nWARNINGS\n")
		for _, warn := range r.Warnings {
			p("- %s\n", warn)
		}
	}
	return tw.Flush()
}
