// Package main — scenario loading + validation for sentinel-bench.
//
// A scenario is a directory under scenarios/ containing scenario.yaml,
// cluster/, ground-truth.diff, and evidence.md. The MANIFEST.yaml at
// scenarios/ pins every scenario's ID, slug, tags, and the SHA256 of its
// ground-truth.diff. The runner refuses to execute scenarios not listed
// in the manifest, and validate refuses to run if a manifest hash drifts.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	yaml "gopkg.in/yaml.v3"
)

// Manifest is the top-level scenarios/MANIFEST.yaml structure.
type Manifest struct {
	Version   int                `yaml:"version"`
	Scenarios []ManifestScenario `yaml:"scenarios"`
}

// ManifestScenario is one entry in scenarios/MANIFEST.yaml.
type ManifestScenario struct {
	ID                string   `yaml:"id"`
	Slug              string   `yaml:"slug"`
	Category          string   `yaml:"category"`
	Severity          string   `yaml:"severity"`
	Difficulty        string   `yaml:"difficulty"`
	Origin            string   `yaml:"origin"`
	GroundTruthSHA256 string   `yaml:"ground_truth_sha256"`
	TimeBudget        Duration `yaml:"time_budget"`
	TokenBudget       int      `yaml:"token_budget"`
	CostBudgetUSD     float64  `yaml:"cost_budget_usd"`
}

// Scenario is a fully-resolved scenario: the manifest entry plus the parsed
// scenario.yaml plus the absolute directory path. The runner operates on
// this type.
type Scenario struct {
	Manifest ManifestScenario
	Dir      string

	ID            string   `yaml:"id"`
	Slug          string   `yaml:"slug"`
	Title         string   `yaml:"title"`
	Category      string   `yaml:"category"`
	Severity      string   `yaml:"severity"`
	Difficulty    string   `yaml:"difficulty"`
	Origin        string   `yaml:"origin"`
	TimeBudget    Duration `yaml:"time_budget"`
	TokenBudget   int      `yaml:"token_budget"`
	CostBudgetUSD float64  `yaml:"cost_budget_usd"`

	Setup      Setup       `yaml:"setup"`
	Trigger    Trigger     `yaml:"trigger"`
	Assertions []Assertion `yaml:"assertions"`

	GroundTruthPath string `yaml:"ground_truth"`
	EvidencePath    string `yaml:"evidence"`
}

// Setup is the setup block of scenario.yaml.
type Setup struct {
	Cluster    ClusterSpec `yaml:"cluster"`
	Apply      []string    `yaml:"apply"`
	SourceRepo string      `yaml:"source_repo"`
	SourceRef  string      `yaml:"source_ref"`
	SourcePath string      `yaml:"source_path"`
}

// ClusterSpec is the cluster block of setup.
type ClusterSpec struct {
	KindVersion       string `yaml:"kind_version"`
	KubernetesVersion string `yaml:"kubernetes_version"`
	Nodes             int    `yaml:"nodes"`
}

// Trigger is the trigger block of scenario.yaml.
type Trigger struct {
	Type  string         `yaml:"type"`
	Alert map[string]any `yaml:"alert"`
}

// Assertion is one entry in the assertions list of scenario.yaml.
type Assertion struct {
	Type      string  `yaml:"type"`
	Weight    float64 `yaml:"weight"`
	Threshold float64 `yaml:"threshold"`
}

// Duration is a time.Duration that unmarshals from a YAML string like
// "15m" or "2h30m". The runner's per-scenario wall-clock budget is a
// Duration so scenario.yaml can stay human-readable.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("decode duration: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// LoadManifest reads and parses scenarios/MANIFEST.yaml.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if len(m.Scenarios) == 0 {
		return nil, fmt.Errorf("manifest %s lists no scenarios", path)
	}
	return &m, nil
}

// FindScenario returns the manifest entry with the given ID, or nil.
func (m *Manifest) FindScenario(id string) *ManifestScenario {
	for i := range m.Scenarios {
		if m.Scenarios[i].ID == id {
			return &m.Scenarios[i]
		}
	}
	return nil
}

// LoadScenario loads a single scenario by its manifest entry. The scenario
// directory is resolved relative to scenariosDir.
func LoadScenario(scenariosDir string, entry ManifestScenario) (*Scenario, error) {
	dirName := fmt.Sprintf("%s-%s", entry.ID, entry.Slug)
	dir := filepath.Join(scenariosDir, dirName)
	scenarioPath := filepath.Join(dir, "scenario.yaml")
	data, err := os.ReadFile(scenarioPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", scenarioPath, err)
	}
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", scenarioPath, err)
	}
	s.Manifest = entry
	s.Dir = dir
	if s.GroundTruthPath == "" {
		s.GroundTruthPath = "ground-truth.diff"
	}
	if s.EvidencePath == "" {
		s.EvidencePath = "evidence.md"
	}
	return &s, nil
}

// LoadAllScenarios loads every scenario listed in the manifest.
func LoadAllScenarios(scenariosDir string) ([]*Scenario, error) {
	manifestPath := filepath.Join(scenariosDir, "MANIFEST.yaml")
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	out := make([]*Scenario, 0, len(m.Scenarios))
	for _, entry := range m.Scenarios {
		s, err := LoadScenario(scenariosDir, entry)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// GroundTruth returns the contents of the scenario's ground-truth.diff.
func (s *Scenario) GroundTruth() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, s.GroundTruthPath))
	if err != nil {
		return "", fmt.Errorf("read ground truth: %w", err)
	}
	return string(data), nil
}

// GroundTruthSHA256 returns the hex-encoded SHA256 of the scenario's
// ground-truth.diff, used by validate to detect drift.
func (s *Scenario) GroundTruthSHA256() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, s.GroundTruthPath))
	if err != nil {
		return "", fmt.Errorf("read ground truth: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ClusterManifestPaths returns the absolute paths of the cluster/*.yaml
// files this scenario applies. Paths come from setup.apply in
// scenario.yaml and are resolved relative to the scenario directory.
func (s *Scenario) ClusterManifestPaths() ([]string, error) {
	if len(s.Setup.Apply) == 0 {
		return nil, fmt.Errorf("scenario %s: setup.apply is empty", s.ID)
	}
	out := make([]string, 0, len(s.Setup.Apply))
	for _, rel := range s.Setup.Apply {
		p := filepath.Join(s.Dir, rel)
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("scenario %s: apply target %s: %w", s.ID, rel, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Validate checks the scenario's structural integrity: required files
// exist, assertions are well-formed, and the ground-truth SHA256 matches
// the manifest entry. Returns nil if the scenario is OK, an error
// describing the first problem otherwise.
func (s *Scenario) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("scenario: id is empty")
	}
	if s.Slug == "" {
		return fmt.Errorf("scenario %s: slug is empty", s.ID)
	}
	if len(s.Assertions) == 0 {
		return fmt.Errorf("scenario %s: no assertions", s.ID)
	}
	var totalWeight float64
	for i, a := range s.Assertions {
		if a.Type == "" {
			return fmt.Errorf("scenario %s: assertion %d has no type", s.ID, i)
		}
		if a.Weight < 0 || a.Weight > 1 {
			return fmt.Errorf("scenario %s: assertion %d weight out of [0,1]: %f", s.ID, i, a.Weight)
		}
		totalWeight += a.Weight
	}
	// We do NOT require weights to sum to 1 — partial-credit scoring works
	// with any positive weights, and "0.25 each" is the common case. We
	// do warn (via stderr in main, not here) if the sum drifts from 1.

	// Required files.
	for _, rel := range []string{s.GroundTruthPath, s.EvidencePath, "scenario.yaml"} {
		p := filepath.Join(s.Dir, rel)
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("scenario %s: missing %s: %w", s.ID, rel, err)
		}
	}
	if len(s.Setup.Apply) == 0 {
		return fmt.Errorf("scenario %s: setup.apply is empty", s.ID)
	}
	for _, rel := range s.Setup.Apply {
		p := filepath.Join(s.Dir, rel)
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("scenario %s: missing apply target %s: %w", s.ID, rel, err)
		}
	}

	// Ground-truth SHA256 must match the manifest entry.
	hash, err := s.GroundTruthSHA256()
	if err != nil {
		return err
	}
	if hash != s.Manifest.GroundTruthSHA256 {
		return fmt.Errorf("scenario %s: ground-truth SHA256 drift: manifest=%s actual=%s (run `sha256sum %s` and update MANIFEST.yaml)",
			s.ID, s.Manifest.GroundTruthSHA256, hash, filepath.Join(s.Dir, s.GroundTruthPath))
	}
	return nil
}

// ServiceName returns the service the alert is about. Used as the
// `service` field in the POST /admin/investigate payload.
func (s *Scenario) ServiceName() string {
	if v, ok := s.Trigger.Alert["labels"].(map[string]any); ok {
		if svc, ok := v["service"].(string); ok && svc != "" {
			return svc
		}
	}
	if v, ok := s.Trigger.Alert["service"].(string); ok && v != "" {
		return v
	}
	return s.Slug
}

// Namespace returns the namespace the alert is about. Used as the
// `namespace` field in the POST /admin/investigate payload.
func (s *Scenario) Namespace() string {
	if v, ok := s.Trigger.Alert["namespace"].(string); ok && v != "" {
		return v
	}
	return "default"
}

// Reason returns the trigger reason for the POST /admin/investigate payload.
func (s *Scenario) Reason() string {
	if v, ok := s.Trigger.Alert["annotations"].(map[string]any); ok {
		if summary, ok := v["summary"].(string); ok && summary != "" {
			return fmt.Sprintf("sentinel-bench: %s", summary)
		}
	}
	return fmt.Sprintf("sentinel-bench: scenario %s", s.ID)
}
