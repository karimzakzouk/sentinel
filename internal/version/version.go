// Package version provides a single source of truth for the Sentinel version.
//
// The VERSION file at the repo root is canonical. scripts/sync-version.py
// rewrites every other place the version appears (Chart.yaml, values.yaml,
// README.md, web/package.json) so they always match. CI enforces this with
// `python3 scripts/sync-version.py --check` on every PR.
//
// The Go binary reads the version from the Version variable, set at build
// time via -ldflags. If unset (e.g. `go run` or `go test`), it falls back
// to "dev".
package version

// Version is the canonical Sentinel version. Set at build time via
// -ldflags="-X github.com/karimzakzouk/sentinel/internal/version.Version=X.Y.Z".
// Defaults to "dev" when unset.
var Version = "dev"
