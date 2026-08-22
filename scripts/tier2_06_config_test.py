#!/usr/bin/env python3
"""Update config_test.go for auto-degrade behavior + add ObserveOnly tests."""
from pathlib import Path

REPO = Path("/home/z/my-project/repos/sentinel")
test_path = REPO / "internal/config/config_test.go"
test = test_path.read_text()

# Replace TestValidateMissingLLMKey — now expects a warning, not an error
old1 = """func TestValidateMissingLLMKey(t *testing.T) {
\tsetEnv(t, map[string]string{
\t\t"SENTINEL_LLM_API_KEY":        "",
\t\t"SENTINEL_LLM_PROVIDER":       "openai",
\t\t"SENTINEL_LLM_FAST_MODEL":     "gpt-4o-mini",
\t\t"SENTINEL_LLM_FRONTIER_MODEL": "gpt-4o",
\t\t"SENTINEL_GITHUB_TOKEN":       "ghp_x",
\t\t"SENTINEL_LLM_PROVIDERS":      "",
\t})
\tc := Load()
\terrs, _ := c.Validate()
\tif len(errs) == 0 {
\t\tt.Fatal("expected at least one error for missing LLM_API_KEY")
\t}
}"""

new1 = """func TestValidateMissingLLMKey(t *testing.T) {
\tsetEnv(t, map[string]string{
\t\t"SENTINEL_LLM_API_KEY":        "",
\t\t"SENTINEL_LLM_PROVIDER":       "openai",
\t\t"SENTINEL_LLM_FAST_MODEL":     "gpt-4o-mini",
\t\t"SENTINEL_LLM_FRONTIER_MODEL": "gpt-4o",
\t\t"SENTINEL_GITHUB_TOKEN":       "ghp_x",
\t\t"SENTINEL_LLM_PROVIDERS":      "",
\t\t"SENTINEL_OBSERVE_ONLY":       "",
\t})
\tc := Load()
\terrs, warns := c.Validate()
\tif len(errs) != 0 {
\t\tt.Fatalf("auto-degrade: expected 0 errors for missing LLM_API_KEY, got %v", errs)
\t}
\tfound := false
\tfor _, w := range warns {
\t\tif contains(w, "SENTINEL_LLM_API_KEY") {
\t\t\tfound = true
\t\t}
\t}
\tif !found {
\t\tt.Errorf("expected warning about LLM_API_KEY, got %v", warns)
\t}
\tif c.IsLLMConfigured() {
\t\tt.Error("IsLLMConfigured() should be false when LLM_API_KEY is empty")
\t}
}"""

assert old1 in test, "test: TestValidateMissingLLMKey not found"
test = test.replace(old1, new1, 1)

# Replace TestValidateMissingGitHubToken — now expects a warning
old2 = """func TestValidateMissingGitHubToken(t *testing.T) {
\tsetEnv(t, map[string]string{
\t\t"SENTINEL_LLM_API_KEY":        "sk-x",
\t\t"SENTINEL_LLM_PROVIDER":       "openai",
\t\t"SENTINEL_LLM_FAST_MODEL":     "gpt-4o-mini",
\t\t"SENTINEL_LLM_FRONTIER_MODEL": "gpt-4o",
\t\t"SENTINEL_GITHUB_TOKEN":       "",
\t\t"SENTINEL_LLM_PROVIDERS":      "",
\t})
\tc := Load()
\terrs, _ := c.Validate()
\tfound := false
\tfor _, e := range errs {
\t\tif contains(e, "SENTINEL_GITHUB_TOKEN") {
\t\t\tfound = true
\t\t}
\t}
\tif !found {
\t\tt.Errorf("expected error about GITHUB_TOKEN, got %v", errs)
\t}
}"""

new2 = """func TestValidateMissingGitHubToken(t *testing.T) {
\tsetEnv(t, map[string]string{
\t\t"SENTINEL_LLM_API_KEY":        "sk-x",
\t\t"SENTINEL_LLM_PROVIDER":       "openai",
\t\t"SENTINEL_LLM_FAST_MODEL":     "gpt-4o-mini",
\t\t"SENTINEL_LLM_FRONTIER_MODEL": "gpt-4o",
\t\t"SENTINEL_GITHUB_TOKEN":       "",
\t\t"SENTINEL_LLM_PROVIDERS":      "",
\t\t"SENTINEL_OBSERVE_ONLY":       "",
\t})
\tc := Load()
\terrs, warns := c.Validate()
\tif len(errs) != 0 {
\t\tt.Fatalf("auto-degrade: expected 0 errors for missing GITHUB_TOKEN, got %v", errs)
\t}
\tfound := false
\tfor _, w := range warns {
\t\tif contains(w, "SENTINEL_GITHUB_TOKEN") {
\t\t\tfound = true
\t\t}
\t}
\tif !found {
\t\tt.Errorf("expected warning about GITHUB_TOKEN, got %v", warns)
\t}
\tif c.IsGitHubConfigured() {
\t\tt.Error("IsGitHubConfigured() should be false when GITHUB_TOKEN is empty")
\t}
}

func TestObserveOnlyFlag(t *testing.T) {
\tsetEnv(t, map[string]string{
\t\t"SENTINEL_LLM_API_KEY":        "sk-x",
\t\t"SENTINEL_LLM_PROVIDER":       "openai",
\t\t"SENTINEL_LLM_FAST_MODEL":     "gpt-4o-mini",
\t\t"SENTINEL_LLM_FRONTIER_MODEL": "gpt-4o",
\t\t"SENTINEL_GITHUB_TOKEN":       "ghp_x",
\t\t"SENTINEL_OBSERVE_ONLY":       "true",
\t})
\tc := Load()
\tif !c.ObserveOnly {
\t\tt.Fatal("ObserveOnly should be true when SENTINEL_OBSERVE_ONLY=true")
\t}
\tif !c.IsObserveOnly() {
\t\tt.Error("IsObserveOnly() should return true")
\t}
\t// Even with creds set, observe-only mode disables them.
\tif c.IsLLMConfigured() {
\t\tt.Error("IsLLMConfigured() should be false in observe-only mode")
\t}
\tif c.IsGitHubConfigured() {
\t\tt.Error("IsGitHubConfigured() should be false in observe-only mode")
\t}
\terrs, warns := c.Validate()
\tif len(errs) != 0 {
\t\tt.Errorf("observe-only mode should produce 0 errors, got %v", errs)
\t}
\tfound := false
\tfor _, w := range warns {
\t\tif contains(w, "observe-only") {
\t\t\tfound = true
\t\t}
\t}
\tif !found {
\t\tt.Errorf("expected observe-only warning, got %v", warns)
\t}
}

func TestAutoDegradeAllEmpty(t *testing.T) {
\tsetEnv(t, map[string]string{
\t\t"SENTINEL_LLM_API_KEY":        "",
\t\t"SENTINEL_LLM_PROVIDER":       "",
\t\t"SENTINEL_LLM_FAST_MODEL":     "",
\t\t"SENTINEL_LLM_FRONTIER_MODEL": "",
\t\t"SENTINEL_GITHUB_TOKEN":       "",
\t\t"SENTINEL_LLM_PROVIDERS":      "",
\t\t"SENTINEL_OBSERVE_ONLY":       "",
\t})
\tc := Load()
\terrs, _ := c.Validate()
\tif len(errs) != 0 {
\t\tt.Fatalf("auto-degrade with all empty: expected 0 errors, got %v", errs)
\t}
\t// Defaults should have been applied for empty LLM fields.
\tif c.LLMProvider != "zai" {
\t\tt.Errorf("LLMProvider should default to 'zai', got %q", c.LLMProvider)
\t}
\tif c.LLMFastModel != "glm-4-flash" {
\t\tt.Errorf("LLMFastModel should default to 'glm-4-flash', got %q", c.LLMFastModel)
\t}
\tif c.LLMFrontierModel != "glm-4.5" {
\t\tt.Errorf("LLMFrontierModel should default to 'glm-4.5', got %q", c.LLMFrontierModel)
\t}
}"""

assert old2 in test, "test: TestValidateMissingGitHubToken not found"
test = test.replace(old2, new2, 1)

test_path.write_text(test)
print("OK: config_test.go — updated for auto-degrade + ObserveOnly tests")
