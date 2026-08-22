#!/usr/bin/env python3
"""Apply Tier 2 #6: auto-degrade config + SENTINEL_OBSERVE_ONLY flag."""
from pathlib import Path

REPO = Path("/home/z/my-project/repos/sentinel")
cfg_path = REPO / "internal/config/config.go"
cfg = cfg_path.read_text()

# 1. Add ObserveOnly field to Config struct (after LogLevel)
old_log = """\t// ─── Logging ─────────────────────────────────────────────────────────
\tLogLevel string
}"""
new_log = """\t// ─── Logging ─────────────────────────────────────────────────────────
\tLogLevel string

\t// ─── Observe-only mode ─────────────────────────────────────────────
\t// ObserveOnly forces Sentinel into a read-only mode: it serves the API
\t// and web UI but does NOT call the LLM, clone repos, open PRs, or execute
\t// any fixes. Useful for demos, evaluation, and "dry-run" deployments.
\t// When true, overrides all capability checks (LLM, GitHub, K8s).
\t// When false (default), Sentinel auto-degrades: if LLM_API_KEY is empty
\t// it uses a noop LLM (logs "would have called"); if GITHUB_TOKEN is empty
\t// it skips the PR step; if no K8s cluster is reachable it serves the API
\t// only.
\tObserveOnly bool
}"""
assert old_log in cfg, "config.go: LogLevel field not found"
cfg = cfg.replace(old_log, new_log, 1)

# 2. Add ObserveOnly to Load()
old_load = """\t\tLogLevel: getenv("SENTINEL_LOG_LEVEL", "INFO"),

\t\tLLMProviders: loadLLMProviders(),"""
new_load = """\t\tLogLevel: getenv("SENTINEL_LOG_LEVEL", "INFO"),

\t\tObserveOnly: getenvBool("SENTINEL_OBSERVE_ONLY", false),

\t\tLLMProviders: loadLLMProviders(),"""
assert old_load in cfg, "config.go: Load() LogLevel line not found"
cfg = cfg.replace(old_load, new_load, 1)

# 3. Replace Validate() — auto-degrade instead of hard-fail
old_validate = """func (c *Config) Validate() (errors []string, warnings []string) {
\tif c.LLMAPIKey == "" {
\t\terrors = append(errors, "SENTINEL_LLM_API_KEY is empty — Sentinel cannot run without an LLM API key")
\t}
\tif c.LLMProvider == "" {
\t\terrors = append(errors, "SENTINEL_LLM_PROVIDER is empty")
\t} else if !isKnownProvider(c.LLMProvider) {
\t\twarnings = append(warnings, fmt.Sprintf(
\t\t\t"SENTINEL_LLM_PROVIDER=%q is not in the known-providers list — "+
\t\t\t\t"if this is a typo, LLM calls will be sent to the wrong endpoint with your API key attached",
\t\t\tc.LLMProvider))
\t}
\tif c.LLMFastModel == "" {
\t\terrors = append(errors, "SENTINEL_LLM_FAST_MODEL is empty")
\t}
\tif c.LLMFrontierModel == "" {
\t\terrors = append(errors, "SENTINEL_LLM_FRONTIER_MODEL is empty")
\t}

\tif c.GitHubToken == "" {
\t\terrors = append(errors, "SENTINEL_GITHUB_TOKEN is empty — Sentinel needs a GitHub PAT to clone repos and open PRs")
\t}

\tif len(c.WriteNamespaces) == 0 {
\t\twarnings = append(warnings, "SENTINEL_WRITE_NAMESPACES is empty — Sentinel cannot write fixes to any namespace")
\t}

\tif c.APIToken == "" {
\t\twarnings = append(warnings, "SENTINEL_API_TOKEN is empty — admin API is unauthenticated. Set this in production.")
\t}

\tif len(c.AllowedGitHubRepos) == 0 && c.GitHubToken != "" {
\t\twarnings = append(warnings, "SENTINEL_ALLOWED_GITHUB_REPOS is empty — Sentinel will clone ANY repo annotated on a pod using the configured GitHub PAT. Set an explicit allowlist.")
\t}

\tif c.SlackBotToken != "" && c.SlackSigningSecret == "" {
\t\twarnings = append(warnings, "SENTINEL_SLACK_SIGNING_SECRET is empty but SLACK_BOT_TOKEN is set — Slack requests will be rejected")
\t}

\tif c.SandboxMaxRetries < 0 {
\t\twarnings = append(warnings, fmt.Sprintf("SENTINEL_SANDBOX_MAX_RETRIES=%d (negative) — clamping to 0", c.SandboxMaxRetries))
\t\tc.SandboxMaxRetries = 0
\t}
\tif c.SandboxMaxRetries > 10 {
\t\twarnings = append(warnings, fmt.Sprintf("SENTINEL_SANDBOX_MAX_RETRIES=%d (very high) — each retry takes ~5-10min", c.SandboxMaxRetries))
\t}
\tif c.InvestigatorTimeoutSec <= 0 {
\t\twarnings = append(warnings, fmt.Sprintf("SENTINEL_INVESTIGATOR_TIMEOUT=%d (non-positive)", c.InvestigatorTimeoutSec))
\t}
\tif c.ConfidenceAutonomous < 0 || c.ConfidenceAutonomous > 1 {
\t\twarnings = append(warnings, fmt.Sprintf("SENTINEL_CONFIDENCE_THRESHOLD_AUTONOMOUS=%.2f out of [0,1] — clamping", c.ConfidenceAutonomous))
\t\tc.ConfidenceAutonomous = clamp01(c.ConfidenceAutonomous)
\t}

\treturn errors, warnings
}"""

new_validate = """// Validate returns blocking errors and non-blocking warnings. In observe-only
// mode (SENTINEL_OBSERVE_ONLY=true) or when capabilities are auto-degraded,
// missing LLM/GitHub/K8s config produces warnings instead of errors.
//
// Sentinel never refuses to start for missing optional config. It starts in
// the most capable mode the available config allows:
//
//\tFull autonomous mode: all env vars set.
//\tObserve-only mode: SENTINEL_OBSERVE_ONLY=true, or all creds empty.
//\tDegraded mode: some creds set, some empty (noop LLM, no-PR, etc.).
func (c *Config) Validate() (errors []string, warnings []string) {
\tif c.ObserveOnly {
\t\twarnings = append(warnings, "SENTINEL_OBSERVE_ONLY=true — running in observe-only mode (no LLM, no GitHub, no K8s writes). The API + web UI are served; no autonomous actions will be taken.")
\t\t// In observe-only mode, everything is optional. Still validate
\t\t// numeric ranges below.
\t} else {
\t\t// Auto-degrade: warn instead of error when creds are missing.
\t\tif c.LLMAPIKey == "" {
\t\t\twarnings = append(warnings, "SENTINEL_LLM_API_KEY is empty — LLM calls will be noop (logged, not sent). Investigations will detect incidents but cannot diagnose or fix them.")
\t\t}
\t\tif c.LLMProvider == "" {
\t\t\twarnings = append(warnings, "SENTINEL_LLM_PROVIDER is empty — defaulting to 'zai'")
\t\t\tc.LLMProvider = "zai"
\t\t} else if !isKnownProvider(c.LLMProvider) {
\t\t\twarnings = append(warnings, fmt.Sprintf(
\t\t\t\t"SENTINEL_LLM_PROVIDER=%q is not in the known-providers list — "+
\t\t\t\t\t"if this is a typo, LLM calls will be sent to the wrong endpoint with your API key attached",
\t\t\t\tc.LLMProvider))
\t\t}
\t\tif c.LLMFastModel == "" {
\t\t\twarnings = append(warnings, "SENTINEL_LLM_FAST_MODEL is empty — defaulting to 'glm-4-flash'")
\t\t\tc.LLMFastModel = "glm-4-flash"
\t\t}
\t\tif c.LLMFrontierModel == "" {
\t\t\twarnings = append(warnings, "SENTINEL_LLM_FRONTIER_MODEL is empty — defaulting to 'glm-4.5'")
\t\t\tc.LLMFrontierModel = "glm-4.5"
\t\t}
\t\tif c.GitHubToken == "" {
\t\t\twarnings = append(warnings, "SENTINEL_GITHUB_TOKEN is empty — Sentinel cannot clone repos or open PRs. Investigations will diagnose but cannot fix.")
\t\t}
\t}

\tif len(c.WriteNamespaces) == 0 {
\t\twarnings = append(warnings, "SENTINEL_WRITE_NAMESPACES is empty — Sentinel cannot write fixes to any namespace")
\t}

\tif c.APIToken == "" {
\t\twarnings = append(warnings, "SENTINEL_API_TOKEN is empty — admin API is unauthenticated. Set this in production.")
\t}

\tif len(c.AllowedGitHubRepos) == 0 && c.GitHubToken != "" {
\t\twarnings = append(warnings, "SENTINEL_ALLOWED_GITHUB_REPOS is empty — Sentinel will clone ANY repo annotated on a pod using the configured GitHub PAT. Set an explicit allowlist.")
\t}

\tif c.SlackBotToken != "" && c.SlackSigningSecret == "" {
\t\twarnings = append(warnings, "SENTINEL_SLACK_SIGNING_SECRET is empty but SLACK_BOT_TOKEN is set — Slack requests will be rejected")
\t}

\tif c.SandboxMaxRetries < 0 {
\t\twarnings = append(warnings, fmt.Sprintf("SENTINEL_SANDBOX_MAX_RETRIES=%d (negative) — clamping to 0", c.SandboxMaxRetries))
\t\tc.SandboxMaxRetries = 0
\t}
\tif c.SandboxMaxRetries > 10 {
\t\twarnings = append(warnings, fmt.Sprintf("SENTINEL_SANDBOX_MAX_RETRIES=%d (very high) — each retry takes ~5-10min", c.SandboxMaxRetries))
\t}
\tif c.InvestigatorTimeoutSec <= 0 {
\t\twarnings = append(warnings, fmt.Sprintf("SENTINEL_INVESTIGATOR_TIMEOUT=%d (non-positive)", c.InvestigatorTimeoutSec))
\t}
\tif c.ConfidenceAutonomous < 0 || c.ConfidenceAutonomous > 1 {
\t\twarnings = append(warnings, fmt.Sprintf("SENTINEL_CONFIDENCE_THRESHOLD_AUTONOMOUS=%.2f out of [0,1] — clamping", c.ConfidenceAutonomous))
\t\tc.ConfidenceAutonomous = clamp01(c.ConfidenceAutonomous)
\t}

\treturn errors, warnings
}

// IsLLMConfigured reports whether the LLM is usable. When false, the LLM
// router will be a noop that logs "would have called LLM" instead of making
// API calls.
func (c *Config) IsLLMConfigured() bool {
\treturn !c.ObserveOnly && c.LLMAPIKey != "" && c.LLMProvider != ""
}

// IsGitHubConfigured reports whether GitHub operations are possible. When
// false, Sentinel detects and diagnoses but cannot open PRs.
func (c *Config) IsGitHubConfigured() bool {
\treturn !c.ObserveOnly && c.GitHubToken != ""
}

// IsObserveOnly reports whether Sentinel is in observe-only mode (no
// autonomous actions, API + UI only).
func (c *Config) IsObserveOnly() bool {
\treturn c.ObserveOnly
}"""

assert old_validate in cfg, "config.go: Validate() not found"
cfg = cfg.replace(old_validate, new_validate, 1)

cfg_path.write_text(cfg)
print("OK: config.go — auto-degrade + ObserveOnly flag")
