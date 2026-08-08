# Contributing to Sentinel

Thanks for your interest in contributing! This document covers the basics.

## Development setup

### Prerequisites

- **Go 1.26+**
- **Node.js 22+** (for the web UI)
- **Helm 3.16+** (for chart development)
- **Docker** (for building images)
- **golangci-lint v2.12+** (for linting)
- A Kubernetes cluster (KinD, Minikube, or a real cluster) for integration testing

### Quick start

```bash
git clone https://github.com/karimzakzouk/sentinel.git
cd sentinel

# Build (includes web UI)
make build

# Build without web UI (faster for Go-only changes)
make build-no-web

# Run tests
make test

# Lint
make lint

# Run locally (needs kubeconfig + env vars)
SENTINEL_GITHUB_TOKEN=ghp_xxx \
SENTINEL_LLM_API_KEY=sk-xxx \
make run
```

### Web UI development

```bash
# Start the Next.js dev server (hot reload)
make web-dev

# Build the web UI for production
make web
```

The web UI is a static export embedded into the Go binary at build time via
`//go:embed all:out`. Run `make web` before `make build` to include the
latest frontend.

## Code style

### Go

- **gofmt** — all code must be gofmt-clean. Run `gofmt -w .` before committing.
- **golangci-lint** — run `make lint` before pushing. The config is in
  `.golangci.yml`.
- **Doc comments** on every exported identifier (function, type, constant,
  variable). One concise sentence explaining what it does.
- **Structured logging** via `logging.Info("msg", "key", value, ...)`. Never
  `log.Printf` or `fmt.Println` in library code.
- **`context.Context` first param** on every function that does I/O.
- **Errors wrapped** with `fmt.Errorf("...: %w", err)` at every boundary.
- **No `panic`** in library code. Only `main` can panic.
- **No commented-out code**. Delete it.
- **No `TODO`/`FIXME`** without an issue link.

### TypeScript / React

- **TypeScript strict** — `strict: true`, `noUncheckedIndexedAccess: true`.
- **No `any` types** — use `unknown` + type guards.
- **Named imports only** for lucide-react icons. No `import * as Icons`.
- **No `console.log`** in committed code (ESLint rule).
- **No `window.prompt`/`window.alert`** — use the shadcn Dialog component.
- **No fabricated data** — if the API doesn't return it, show "No data".
- **ESLint** — run `npx eslint .` before pushing. Config in `eslint.config.mjs`.
- **Vitest** — run `npx vitest run` before pushing. Tests in `src/__tests__/`.

## Testing

### Go tests

```bash
# All tests with race detector
make test

# A specific package
go test ./internal/agents -race -count=1

# With coverage
make test-coverage
```

Every package must have tests. Aim for >80% coverage on pure-logic packages
(agents, llm, feedback, safety, automerge). Integration packages (k8s, github,
executors) can have lower coverage if the logic is in the pure packages.

### Frontend tests

```bash
cd web
npx vitest run
```

Tests live in `web/src/__tests__/`. Run them before pushing.

### Helm chart

```bash
make helm-lint
helm template test chart/ > /tmp/rendered.yaml
test $(grep -c "kind:" /tmp/rendered.yaml) -ge 6
```

## Versioning

The `VERSION` file is the single source of truth. `scripts/sync-version.py`
keeps every other file in sync (Chart.yaml, values.yaml, README, web/package.json).

```bash
# Check if everything is in sync (CI runs this on every PR)
make check-version

# Bump version + sync all files
make bump-version VERSION=1.1.0

# Full release: bump + commit + tag + push
make release VERSION=1.1.0
```

**Never manually edit** the version in Chart.yaml, values.yaml, README.md, or
web/package.json. Always use `make bump-version`.

## Pull requests

1. Fork the repo and create a branch: `git checkout -b my-feature`
2. Make your changes. Keep commits focused — one logical change per commit.
3. Run `make ci` (lint + test + vet + helm-lint + check-version).
4. Push and open a PR. Use the PR template.
5. Ensure CI passes. If a check fails, fix it.

### Commit messages

Follow the conventional-commits style:

```
type(scope): brief description

Optional longer description.
```

Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`, `build`.
Scopes: package name (e.g. `agents`, `llm`, `chart`, `ci`).

Examples:
```
feat(agents): add Rust stack-trace parser
fix(llm): race in pool credentials swap
docs(readme): update install command
ci(trivy): pin to v0.36.0
```

## Architecture

See the [architecture section in the README](README.md#architecture) for the
package map. Each package has a `doc.go` with a package-level doc comment
explaining what it does and why it exists.

## Adding a new LLM provider

1. Add the provider to `internal/llm/goai_provider.go` — add a case to the
   switch in `buildGoAIModel`.
2. Add the provider to the `isKnownProvider` list in `internal/config/config.go`.
3. Add the provider to the README's LLM provider table.
4. Test with `SENTINEL_LLM_PROVIDER=newprovider go test ./internal/llm`.

## Adding a new chatops command

1. Add the command to `internal/chatops/types.go` — define the Command struct
   with Name, Tier, Permission, Description, Usage.
2. Register it in `internal/chatops/registry.go`.
3. Implement the handler in `internal/chatops/commands.go` or `commands_more.go`.
4. The `/api/meta` endpoint automatically serves it to the frontend.

## Adding a sentinel-bench scenario

1. `mkdir sentinel-bench/scenarios/Sxxx-<slug>`
2. Fill in `scenario.yaml`, `cluster/`, `ground-truth.diff`, `evidence.md`.
3. Compute the SHA256: `sha256sum ground-truth.diff`
4. Add an entry to `sentinel-bench/scenarios/MANIFEST.yaml`.
5. Run `sentinel-bench validate` to verify.

## License

By contributing, you agree that your contributions will be licensed under the
MIT License.
