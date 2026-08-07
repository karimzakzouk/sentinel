package config

import (
	"os"
	"testing"
)

func setEnv(t *testing.T, kvs map[string]string) {
	t.Helper()
	for k, v := range kvs {
		t.Setenv(k, v)
	}
}

func TestLoadDefaults(t *testing.T) {
	for _, k := range []string{
		"SENTINEL_LLM_API_KEY", "SENTINEL_LLM_PROVIDER", "SENTINEL_LLM_FAST_MODEL",
		"SENTINEL_LLM_FRONTIER_MODEL", "SENTINEL_GITHUB_TOKEN",
		"SENTINEL_LLM_PROVIDERS", "SENTINEL_ALLOWED_GITHUB_REPOS",
		"SENTINEL_WRITE_NAMESPACES",
	} {
		t.Setenv(k, "")
	}

	c := Load()
	if c.SQLitePath != "/data/sentinel.db" {
		t.Errorf("SQLitePath = %q", c.SQLitePath)
	}
	if c.LLMProvider != "zai" {
		t.Errorf("LLMProvider = %q, want zai", c.LLMProvider)
	}
	if c.SandboxEnabled != true {
		t.Errorf("SandboxEnabled default = false, want true")
	}
	if c.SandboxRequired != false {
		t.Errorf("SandboxRequired default = true, want false")
	}
	if c.MaxConcurrentInvestigations != 3 {
		t.Errorf("MaxConcurrentInvestigations = %d, want 3", c.MaxConcurrentInvestigations)
	}
}

func TestValidateMissingLLMKey(t *testing.T) {
	setEnv(t, map[string]string{
		"SENTINEL_LLM_API_KEY":        "",
		"SENTINEL_LLM_PROVIDER":       "openai",
		"SENTINEL_LLM_FAST_MODEL":     "gpt-4o-mini",
		"SENTINEL_LLM_FRONTIER_MODEL": "gpt-4o",
		"SENTINEL_GITHUB_TOKEN":       "ghp_x",
		"SENTINEL_LLM_PROVIDERS":      "",
	})
	c := Load()
	errs, _ := c.Validate()
	if len(errs) == 0 {
		t.Fatal("expected at least one error for missing LLM_API_KEY")
	}
}

func TestValidateMissingGitHubToken(t *testing.T) {
	setEnv(t, map[string]string{
		"SENTINEL_LLM_API_KEY":        "sk-x",
		"SENTINEL_LLM_PROVIDER":       "openai",
		"SENTINEL_LLM_FAST_MODEL":     "gpt-4o-mini",
		"SENTINEL_LLM_FRONTIER_MODEL": "gpt-4o",
		"SENTINEL_GITHUB_TOKEN":       "",
		"SENTINEL_LLM_PROVIDERS":      "",
	})
	c := Load()
	errs, _ := c.Validate()
	found := false
	for _, e := range errs {
		if contains(e, "SENTINEL_GITHUB_TOKEN") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error about GITHUB_TOKEN, got %v", errs)
	}
}

func TestValidateWarnings(t *testing.T) {
	setEnv(t, map[string]string{
		"SENTINEL_LLM_API_KEY":          "sk-x",
		"SENTINEL_LLM_PROVIDER":         "openai",
		"SENTINEL_LLM_FAST_MODEL":       "gpt-4o-mini",
		"SENTINEL_LLM_FRONTIER_MODEL":   "gpt-4o",
		"SENTINEL_GITHUB_TOKEN":         "ghp_x",
		"SENTINEL_LLM_PROVIDERS":        "",
		"SENTINEL_API_TOKEN":            "",
		"SENTINEL_ALLOWED_GITHUB_REPOS": "",
		"SENTINEL_WRITE_NAMESPACES":     "",
	})
	c := Load()
	_, warns := c.Validate()
	if len(warns) == 0 {
		t.Fatal("expected warnings, got none")
	}
}

func TestValidateUnknownProvider(t *testing.T) {
	setEnv(t, map[string]string{
		"SENTINEL_LLM_API_KEY":        "sk-x",
		"SENTINEL_LLM_PROVIDER":       "acme-llm",
		"SENTINEL_LLM_FAST_MODEL":     "m",
		"SENTINEL_LLM_FRONTIER_MODEL": "m",
		"SENTINEL_GITHUB_TOKEN":       "ghp_x",
	})
	c := Load()
	_, warns := c.Validate()
	found := false
	for _, w := range warns {
		if contains(w, "acme-llm") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warning about unknown provider, got %v", warns)
	}
}

func TestCanWriteTo(t *testing.T) {
	c := &Config{WriteNamespaces: []string{"sim", "staging"}}
	if !c.CanWriteTo("sim") {
		t.Error("expected CanWriteTo(sim) = true")
	}
	if c.CanWriteTo("prod") {
		t.Error("expected CanWriteTo(prod) = false")
	}
	c2 := &Config{}
	if c2.CanWriteTo("anything") {
		t.Error("empty WriteNamespaces should deny everything")
	}
}

func TestIsProductionNamespace(t *testing.T) {
	c := &Config{ProductionNamespaces: []string{"prod", "production"}}
	if !c.IsProductionNamespace("prod") {
		t.Error("expected prod to be production")
	}
	if c.IsProductionNamespace("dev") {
		t.Error("expected dev to not be production")
	}
}

func TestIsRepoAllowed(t *testing.T) {
	c := &Config{AllowedGitHubRepos: []string{"acme/api", "Acme/web.git"}}
	cases := []struct {
		repo string
		want bool
	}{
		{"acme/api", true},
		{"ACME/API", true},
		{"acme/api.git", true},
		{"acme/web", true},
		{"acme/other", false},
	}
	for _, tc := range cases {
		t.Run(tc.repo, func(t *testing.T) {
			if got := c.IsRepoAllowed(tc.repo); got != tc.want {
				t.Errorf("IsRepoAllowed(%q) = %v, want %v", tc.repo, got, tc.want)
			}
		})
	}
}

func TestIsRepoAllowedEmptyAllowsAll(t *testing.T) {
	c := &Config{}
	if !c.IsRepoAllowed("anyone/anything") {
		t.Error("empty allowlist should allow all")
	}
}

func TestResolvedBaseURL(t *testing.T) {
	cases := []struct {
		provider string
		baseURL  string
		want     string
	}{
		{"openai", "", "https://api.openai.com/v1"},
		{"zai", "", "https://api.z.ai/api/paas/v4"},
		{"gemini", "", "https://generativelanguage.googleapis.com/v1beta/openai"},
		{"ollama", "", "http://ollama:11434/v1"},
		{"custom", "https://my.api/v1", "https://my.api/v1"},
		{"unknown", "", "https://api.z.ai/api/paas/v4"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			c := &Config{LLMProvider: tc.provider, LLMBaseURL: tc.baseURL}
			if got := c.ResolvedBaseURL(); got != tc.want {
				t.Errorf("ResolvedBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLoadLLMProviders(t *testing.T) {
	t.Setenv("SENTINEL_LLM_PROVIDERS", "")
	if got := loadLLMProviders(); got != nil {
		t.Errorf("empty env should produce nil, got %v", got)
	}

	t.Setenv("SENTINEL_LLM_PROVIDERS", `[invalid json`)
	if got := loadLLMProviders(); got != nil {
		t.Errorf("invalid JSON should produce nil, got %v", got)
	}

	t.Setenv("SENTINEL_LLM_PROVIDERS", `[
		{"id":"or-1","provider":"openrouter","apiKey":"sk-or-x","priority":1},
		{"id":"bad-1","provider":"","apiKey":"k","priority":2},
		{"id":"bad-2","provider":"openai","apiKey":"","priority":3},
		{"id":"ollama-1","provider":"ollama","apiKey":"","priority":4}
	]`)
	got := loadLLMProviders()
	if len(got) != 2 {
		t.Fatalf("expected 2 valid providers, got %d: %+v", len(got), got)
	}
	if got[0].ID != "or-1" || got[1].ID != "ollama-1" {
		t.Errorf("expected or-1 + ollama-1, got %s + %s", got[0].ID, got[1].ID)
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"a, b ,c ", []string{"a", "b", "c"}},
		{",,,", nil},
	}
	for _, tc := range cases {
		got := splitCSV(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitCSV(%q) len = %d, want %d", tc.in, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitCSV(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestGetenvHelpers(t *testing.T) {
	t.Setenv("T_STR", "hello")
	if got := getenv("T_STR", "def"); got != "hello" {
		t.Errorf("getenv = %q", got)
	}
	if got := getenv("T_MISSING", "def"); got != "def" {
		t.Errorf("getenv missing = %q", got)
	}

	t.Setenv("T_INT", "42")
	if got := getenvInt("T_INT", 0); got != 42 {
		t.Errorf("getenvInt = %d", got)
	}
	if got := getenvInt("T_BAD_INT", 7); got != 7 {
		t.Errorf("getenvInt missing = %d", got)
	}

	t.Setenv("T_BOOL_T", "true")
	if !getenvBool("T_BOOL_T", false) {
		t.Error("getenvBool true")
	}
	t.Setenv("T_BOOL_F", "0")
	if getenvBool("T_BOOL_F", true) {
		t.Error("getenvBool 0")
	}
	if !getenvBool("T_BOOL_MISSING", true) {
		t.Error("getenvBool default")
	}
}

func TestClamp01(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{-1, 0},
		{0, 0},
		{0.5, 0.5},
		{1, 1},
		{1.5, 1},
	}
	for _, c := range cases {
		if got := clamp01(c.in); got != c.want {
			t.Errorf("clamp01(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMain(m *testing.M) {
	// Ensure no env leakage from prior test runs affects LoadDefaults.
	os.Exit(m.Run())
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
