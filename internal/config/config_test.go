package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	c, err := Load(write(t, "namespaces: [shop, payments]\nledger: {path: /var/lib/sentinel/ledger.db}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Capabilities.Interval.Duration != 10*time.Minute || c.HealthAddr != ":8081" {
		t.Fatalf("defaults not applied: %+v", c)
	}
}

func TestLoadRejectsUnsafeOrAmbiguousConfig(t *testing.T) {
	cases := map[string]string{
		"no namespaces":         "ledger: {path: /l.db}\n",
		"wildcard namespace":    "namespaces: ['*']\nledger: {path: /l.db}\n",
		"duplicate namespace":   "namespaces: [a, a]\nledger: {path: /l.db}\n",
		"relative ledger path":  "namespaces: [a]\nledger: {path: l.db}\n",
		"typo in field":         "namespaces: [a]\nledger: {path: /l.db}\nnamespacess: [b]\n",
		"lab ns is watched":     "namespaces: [a]\nlabNamespaces: [a]\nledger: {path: /l.db}\n",
		"interval too short":    "namespaces: [a]\nledger: {path: /l.db}\ncapabilities: {interval: 5s}\n",
		"slot without champion": "namespaces: [a]\nledger: {path: /l.db}\nslots: {brain.classify: {shadows: [x]}}\n",
		"samples under a tick":  "namespaces: [a]\nledger: {path: /l.db}\nlogs: {sampleInterval: 5s}\n",
		"samples over a window": "namespaces: [a]\nledger: {path: /l.db}\nlogs: {sampleInterval: 10m}\n",
		"negative warm-up":      "namespaces: [a]\nledger: {path: /l.db}\ndetection: {warmUp: -1m}\n",
		"capture without token": "namespaces: [a]\nledger: {path: /l.db}\ncapture: {address: ':4318'}\n",
	}
	for name, content := range cases {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: Load accepted it", name)
		}
	}
}

func TestErrorSampling(t *testing.T) {
	on, err := Load(write(t, "namespaces: [a]\nledger: {path: /l.db}\n"))
	if err != nil {
		t.Fatal(err)
	}
	off, err := Load(write(t, "namespaces: [a]\nledger: {path: /l.db}\nlogs: {sampleInterval: 0s}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if on.Logs.SampleEvery() != time.Minute || off.Logs.SampleEvery() != 0 || on.Detection.WarmUp.Duration != 30*time.Minute {
		t.Fatalf("sampling every %v by default and %v when off, warm-up %v", on.Logs.SampleEvery(), off.Logs.SampleEvery(),
			on.Detection.WarmUp.Duration)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	c := &Config{Namespaces: []string{"Bad_NS"}, Ledger: Ledger{Path: "relative"}}
	c.setDefaults()
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "Bad_NS") || !strings.Contains(err.Error(), "ledger.path") {
		t.Fatalf("Validate = %v; want both problems reported", err)
	}
}
