// Package config loads and validates the controller's configuration. It fails
// closed: anything missing or ambiguous is an error, never a default that
// widens what Sentinel may do.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	"github.com/karimzakzouk/sentinel/internal/slot"
)

// Config is the controller configuration file.
type Config struct {
	// Namespaces Sentinel may watch. There is no wildcard.
	Namespaces []string `json:"namespaces"`
	// AgentNamespace is where Sentinel's own workloads run.
	AgentNamespace string `json:"agentNamespace,omitempty"`
	// LabNamespaces is the pool of namespaces created at install for Lab runs.
	LabNamespaces []string `json:"labNamespaces,omitempty"`
	// VerifyRollbacks asks the Lab whether each proposed rollback fixes its
	// incident.
	VerifyRollbacks bool `json:"verifyRollbacks,omitempty"`
	// FixerNamespace, when set, is where the Fixer takes Fix requests: the
	// controller asks it to make each verified rollback hold at its source.
	FixerNamespace string `json:"fixerNamespace,omitempty"`
	// ActorUser is the identity that executes actions.
	ActorUser    string                 `json:"actorUser,omitempty"`
	Ledger       Ledger                 `json:"ledger"`
	Capabilities Capabilities           `json:"capabilities,omitempty"`
	Detection    Detection              `json:"detection,omitempty"`
	Logs         Logs                   `json:"logs,omitempty"`
	UI           UI                     `json:"ui,omitempty"`
	HealthAddr   string                 `json:"healthAddr,omitempty"`
	Slots        map[string]slot.Config `json:"slots,omitempty"`
}

// Ledger configures the event log.
type Ledger struct {
	Path string `json:"path"`
}

// Capabilities configures capability detection.
type Capabilities struct {
	// Interval between capability reports.
	Interval metav1.Duration `json:"interval,omitempty"`
}

// Detection configures the count detector. The budget is the only knob that
// sets thresholds; the window and tick only trade detection speed for noise.
type Detection struct {
	// FalseAlarmsPerWeek is the accepted false alarms per workload per week.
	FalseAlarmsPerWeek float64         `json:"falseAlarmsPerWeek,omitempty"`
	Window             metav1.Duration `json:"window,omitempty"`
	Tick               metav1.Duration `json:"tick,omitempty"`
	// HalfLife is how fast old history fades from the baselines.
	HalfLife metav1.Duration `json:"halfLife,omitempty"`
	// IncidentGap is how long an incident stays open after its signals go
	// quiet; signals within it rejoin the same incident.
	IncidentGap metav1.Duration `json:"incidentGap,omitempty"`
	// WarmUp is how long a workload's sampled error series learns before it
	// may alarm: its usual error volume, unlike its usual crash count, is
	// rarely zero.
	WarmUp metav1.Duration `json:"warmUp,omitempty"`
}

// Logs configures log evidence: container logs fetched, within a budget, when
// a container crashes or a pod stays unready, and every workload's errors
// sampled.
type Logs struct {
	// Enabled defaults to true.
	Enabled *bool `json:"enabled,omitempty"`
	// SampleInterval is how often each workload's errors are sampled from
	// one ready pod. It defaults to a minute; 0s turns sampling off.
	SampleInterval *metav1.Duration `json:"sampleInterval,omitempty"`
}

// SampleEvery returns the sampling interval, zero when sampling is off.
func (l Logs) SampleEvery() time.Duration {
	if l.SampleInterval == nil {
		return time.Minute
	}
	return l.SampleInterval.Duration
}

// On reports whether log evidence is enabled.
func (l Logs) On() bool { return l.Enabled == nil || *l.Enabled }

// UI configures the read-only web UI and API.
type UI struct {
	// Address to listen on. Keep it on loopback: kubectl port-forward reaches
	// it, and nothing else should.
	Address string `json:"address,omitempty"`
}

// Load reads, defaults and validates a config file. Unknown fields are
// rejected so a typo cannot silently disable a setting.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's command line
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.setDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) setDefaults() {
	if c.Capabilities.Interval.Duration == 0 {
		c.Capabilities.Interval.Duration = 10 * time.Minute
	}
	if c.HealthAddr == "" {
		c.HealthAddr = ":8081"
	}
	if c.UI.Address == "" {
		c.UI.Address = "127.0.0.1:8090"
	}
	d := &c.Detection
	if d.FalseAlarmsPerWeek == 0 {
		d.FalseAlarmsPerWeek = 1
	}
	if d.Window.Duration == 0 {
		d.Window.Duration = 5 * time.Minute
	}
	if d.Tick.Duration == 0 {
		d.Tick.Duration = 15 * time.Second
	}
	if d.HalfLife.Duration == 0 {
		d.HalfLife.Duration = 7 * 24 * time.Hour
	}
	if d.IncidentGap.Duration == 0 {
		d.IncidentGap.Duration = 5 * time.Minute
	}
	if d.WarmUp.Duration == 0 {
		d.WarmUp.Duration = 30 * time.Minute
	}
}

// Validate reports every problem at once.
func (c *Config) Validate() error {
	var errs []error
	if len(c.Namespaces) == 0 {
		errs = append(errs, errors.New("namespaces is empty: list the namespaces Sentinel may watch"))
	}
	seen := map[string]bool{}
	for _, ns := range c.Namespaces {
		if msgs := validation.IsDNS1123Label(ns); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("namespace %q is invalid: %v", ns, msgs))
		}
		if seen[ns] {
			errs = append(errs, fmt.Errorf("namespace %q is listed twice", ns))
		}
		seen[ns] = true
	}
	for _, ns := range append([]string{c.AgentNamespace}, c.LabNamespaces...) {
		if ns == "" {
			continue
		}
		if msgs := validation.IsDNS1123Label(ns); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("namespace %q is invalid: %v", ns, msgs))
		}
		if seen[ns] {
			errs = append(errs, fmt.Errorf("namespace %q must not be both a watched workload namespace and one of Sentinel's own", ns))
		}
	}
	if c.Ledger.Path == "" || !filepath.IsAbs(c.Ledger.Path) {
		errs = append(errs, fmt.Errorf("ledger.path %q must be an absolute path", c.Ledger.Path))
	}
	if c.Capabilities.Interval.Duration < time.Minute {
		errs = append(errs, fmt.Errorf("capabilities.interval %s is shorter than 1m", c.Capabilities.Interval.Duration))
	}
	d := c.Detection
	if d.FalseAlarmsPerWeek <= 0 || d.FalseAlarmsPerWeek > 100 {
		errs = append(errs, fmt.Errorf("detection.falseAlarmsPerWeek %v is outside (0, 100]", d.FalseAlarmsPerWeek))
	}
	if s := c.Logs.SampleEvery(); s != 0 && (s < d.Tick.Duration || s > d.Window.Duration) {
		errs = append(errs, fmt.Errorf("logs.sampleInterval %s must be 0s, for off, or between the tick (%s) and the window (%s)",
			s, d.Tick.Duration, d.Window.Duration))
	}
	if d.WarmUp.Duration < 0 {
		errs = append(errs, fmt.Errorf("detection.warmUp %s is negative", d.WarmUp.Duration))
	}
	if d.Tick.Duration < time.Second || d.Window.Duration < 2*d.Tick.Duration || d.HalfLife.Duration < d.Window.Duration {
		errs = append(errs, fmt.Errorf("detection needs tick >= 1s, window >= 2 ticks and halfLife >= window (got %s, %s, %s)",
			d.Tick.Duration, d.Window.Duration, d.HalfLife.Duration))
	}
	for name, s := range c.Slots {
		if s.Champion == "" {
			errs = append(errs, fmt.Errorf("slots.%s has no champion", name))
		}
	}
	return errors.Join(errs...)
}
