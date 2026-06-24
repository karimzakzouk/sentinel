# Contributing to Sentinel

Sentinel is an autonomous AI SRE — one Go binary, one pod, ~28MB. It detects incidents in real-time, clones your repo, diagnoses the root cause with an LLM, writes a fix, verifies it in a sandbox, and opens a Pull Request.

## Quick Start

```bash
git clone https://github.com/karimzakzouk/sentinel.git
cd sentinel

# Build (includes web UI if Node.js is available)
make build

# Or build without web UI (faster)
make build-no-web

# Run tests
make test

# Run locally (needs kubeconfig)
make run

# Lint
make lint

# Helm chart validation
make helm-lint
```

## Architecture

```
cmd/sentinel/          # Server binary (entrypoint)
cmd/sentinelctl/       # CLI binary (thin HTTP client)
internal/
  detection/           # Live log streaming + pod status watcher
  agents/              # 4-agent pipeline (Investigator → Evidence → Analyst → Fix)
  feedback/            # SQLite store + HNSW vector memory + outcome watcher
  executors/           # GitHub PRs, Kaniko builds, sandbox verification
  safety/              # 5 embedded Go rules (replaces OPA)
  correlation/         # Cross-service cascade detection
  depanalysis/         # Shared library dependency analysis
  prreview/            # Predictive PR review (GitHub webhook)
  slackbot/            # Interactive Slack bot
  github/              # GitHub App authentication
  config/              # Env var loading
  llm/                 # OpenAI-compatible LLM client (retry/backoff)
  logging/             # Structured logging (slog)
  metrics/             # Prometheus metrics
  api/                 # HTTP API + SSE + web UI serving
  web/                 # Embedded Next.js dashboard
```

## Adding a New Error Pattern

Edit `internal/agents/error_parser.go`:

```go
// Add your pattern to the regex list
var myErrorRe = regexp.MustCompile(`myError:\s*(.+)`)

// Add detection logic to parseErrorTypeLine()
// Add language detection to detectLanguage()
// Add hypothesis context to generateContextForError()
```

Then add test cases to `internal/agents/error_parser_test.go`.

## Adding a New LLM Provider

Providers are auto-resolved from the `SENTINEL_LLM_PROVIDER` env var in `internal/config/config.go`. Any OpenAI-compatible endpoint works. To add a new built-in:

1. Add the provider name to `ResolvedBaseURL()` in `config.go`
2. Set the base URL
3. Test with `SENTINEL_LLM_PROVIDER=yourprovider make run`

## Adding a New Agent

1. Create `internal/agents/my_agent.go`
2. Implement the agent's `Run(ctx, state) error` method
3. Add it to the pipeline in `internal/agents/pipeline.go`
4. Add panic recovery: `defer recoverPanic("my_agent")`
5. Add a timeout in the pipeline's `Run()` method

## Testing

```bash
make test                    # All tests with race detector
make test-coverage           # Coverage report
go test ./internal/agents/ -v -run TestParseError  # Specific test
```

All tests use stdlib `testing` — no testify, no ginkgo, no gomock.

## CI

Every push to `main` and every PR runs:
- golangci-lint (v2)
- go test -race
- go vet
- govulncheck (CVE scan)
- helm lint

Every tag (`v3.*.*`) triggers:
- Docker build (amd64 + arm64, multi-stage with web UI)
- Push to Docker Hub + GHCR
- Helm chart package + publish to GitHub Pages

## Code Style

- Go idiomatic. No interfaces until you need them.
- `log/slog` for structured logging (JSON to stdout).
- No external dependencies unless absolutely necessary (currently 6 direct deps).
- Error handling: log + continue for non-critical, return for critical.
- Every goroutine has panic recovery.

## License

MIT. See [LICENSE](LICENSE).
