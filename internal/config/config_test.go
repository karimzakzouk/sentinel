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
		"probe ns is watched":   "namespaces: [a]\nprobeNamespace: a\nledger: {path: /l.db}\n",
		"interval too short":    "namespaces: [a]\nledger: {path: /l.db}\ncapabilities: {interval: 5s}\n",
		"slot without champion": "namespaces: [a]\nledger: {path: /l.db}\nslots: {brain.classify: {shadows: [x]}}\n",
	}
	for name, content := range cases {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: Load accepted it", name)
		}
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
