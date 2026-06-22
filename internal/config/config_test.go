package config

import (
	"os"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// Clear all env vars to test defaults
	for _, k := range []string{
		"SENTINEL_LLM_PROVIDER", "SENTINEL_LLM_BASE_URL", "SENTINEL_API_HOST",
		"SENTINEL_WRITE_NAMESPACES", "SENTINEL_PRODUCTION_NAMESPACES",
	} {
		os.Unsetenv(k)
	}

	c := Load()
	if c.LLMProvider != "zai" {
		t.Errorf("LLMProvider = %q, want zai", c.LLMProvider)
	}
	if c.SQLitePath != "/data/sentinel.db" {
		t.Errorf("SQLitePath = %q, want /data/sentinel.db", c.SQLitePath)
	}
	if c.ConfidenceAutonomous != 0.92 {
		t.Errorf("ConfidenceAutonomous = %v, want 0.92", c.ConfidenceAutonomous)
	}
}

func TestResolvedBaseURL(t *testing.T) {
	cases := []struct {
		provider, baseURL, want string
	}{
		{"openai", "", "https://api.openai.com/v1"},
		{"zai", "", "https://api.z.ai/api/paas/v4"},
		{"gemini", "", "https://generativelanguage.googleapis.com/v1beta/openai"},
		{"custom", "https://openrouter.ai/api/v1", "https://openrouter.ai/api/v1"},
		{"unknown", "", "https://api.z.ai/api/paas/v4"}, // fallback
	}
	for _, c := range cases {
		cfg := &Config{LLMProvider: c.provider, LLMBaseURL: c.baseURL}
		got := cfg.ResolvedBaseURL()
		if got != c.want {
			t.Errorf("provider=%q baseURL=%q: got %q, want %q", c.provider, c.baseURL, got, c.want)
		}
	}
}

func TestCanWriteTo(t *testing.T) {
	c := &Config{WriteNamespaces: []string{"sim", "staging"}}
	if !c.CanWriteTo("sim") {
		t.Error("should be able to write to sim")
	}
	if c.CanWriteTo("prod") {
		t.Error("should NOT be able to write to prod")
	}
	c.WriteNamespaces = nil
	if c.CanWriteTo("anything") {
		t.Error("empty WriteNamespaces should deny everything")
	}
}

func TestIsProductionNamespace(t *testing.T) {
	c := &Config{ProductionNamespaces: []string{"prod", "production", "payments"}}
	if !c.IsProductionNamespace("prod") {
		t.Error("prod should be production")
	}
	if c.IsProductionNamespace("dev") {
		t.Error("dev should NOT be production")
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"sim", []string{"sim"}},
		{"sim,staging", []string{"sim", "staging"}},
		{" sim , staging , ", []string{"sim", "staging"}},
	}
	for _, c := range cases {
		got := splitCSV(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitCSV(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitCSV(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}
