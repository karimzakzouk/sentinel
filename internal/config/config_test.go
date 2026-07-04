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

        // to "" (). The previous default was OpenAI-specific but the
        // default provider is "zai", so every default install silently fell back
        // to hash embeddings. Empty default is honest about what's happening.
        if c.LLMEmbeddingModel != "" {
                t.Errorf("LLMEmbeddingModel = %q, want \"\" (empty — hash fallback by default)", c.LLMEmbeddingModel)
        }

        if c.LLMEmbeddingDim != 1536 {
                t.Errorf("LLMEmbeddingDim = %d, want 1536", c.LLMEmbeddingDim)
        }
        if c.InvestigatorTimeoutSec != 120 {
                t.Errorf("InvestigatorTimeoutSec = %d, want 120", c.InvestigatorTimeoutSec)
        }
        if c.ConfidenceLoopBackThreshold != 0.3 {
                t.Errorf("ConfidenceLoopBackThreshold = %v, want 0.3", c.ConfidenceLoopBackThreshold)
        }
        if c.OutcomeWatchDurationMin != 10 {
                t.Errorf("OutcomeWatchDurationMin = %d, want 10", c.OutcomeWatchDurationMin)
        }
        if c.ScaleFixDefaultReplicas != 2 {
                t.Errorf("ScaleFixDefaultReplicas = %d, want 2", c.ScaleFixDefaultReplicas)
        }
        if c.LLMPriorWeight != 0.4 {
                t.Errorf("LLMPriorWeight = %v, want 0.4", c.LLMPriorWeight)
        }
        if len(c.SandboxProbePaths) != 3 {
                t.Errorf("len(SandboxProbePaths) = %d, want 3", len(c.SandboxProbePaths))
        }
        if c.LLMMaxRetries != 3 {
                t.Errorf("LLMMaxRetries = %d, want 3", c.LLMMaxRetries)
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

// "payments" (). The demo's "payments" namespace leaked into every
// install — operators with a namespace literally named "payments" got
// stricter governance than intended.
func TestDefaultProductionNamespacesExcludesPayments(t *testing.T) {
        os.Unsetenv("SENTINEL_PRODUCTION_NAMESPACES")
        c := Load()
        for _, ns := range c.ProductionNamespaces {
                if ns == "payments" {
                        t.Errorf("default ProductionNamespaces should NOT include 'payments' (audit #5), got %v", c.ProductionNamespaces)
                }
        }
        // Should still include prod + production
        hasProd, hasProduction := false, false
        for _, ns := range c.ProductionNamespaces {
                if ns == "prod" {
                        hasProd = true
                }
                if ns == "production" {
                        hasProduction = true
                }
        }
        if !hasProd || !hasProduction {
                t.Errorf("default ProductionNamespaces should include prod+production, got %v", c.ProductionNamespaces)
        }
}

