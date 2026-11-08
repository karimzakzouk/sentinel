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
	// ProbeNamespace holds dry-run capability probes.
	ProbeNamespace string `json:"probeNamespace,omitempty"`
	// ActorUser is the identity that executes actions.
	ActorUser    string                 `json:"actorUser,omitempty"`
	Ledger       Ledger                 `json:"ledger"`
	Capabilities Capabilities           `json:"capabilities,omitempty"`
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
	if c.ProbeNamespace != "" {
		if msgs := validation.IsDNS1123Label(c.ProbeNamespace); len(msgs) > 0 {
			errs = append(errs, fmt.Errorf("probeNamespace %q is invalid: %v", c.ProbeNamespace, msgs))
		}
		if seen[c.ProbeNamespace] {
			errs = append(errs, fmt.Errorf("probeNamespace %q must not be a watched workload namespace", c.ProbeNamespace))
		}
	}
	if c.Ledger.Path == "" || !filepath.IsAbs(c.Ledger.Path) {
		errs = append(errs, fmt.Errorf("ledger.path %q must be an absolute path", c.Ledger.Path))
	}
	if c.Capabilities.Interval.Duration < time.Minute {
		errs = append(errs, fmt.Errorf("capabilities.interval %s is shorter than 1m", c.Capabilities.Interval.Duration))
	}
	for name, s := range c.Slots {
		if s.Champion == "" {
			errs = append(errs, fmt.Errorf("slots.%s has no champion", name))
		}
	}
	return errors.Join(errs...)
}
