// Package version provides a single source of truth for the Sentinel version.
//
// v3.8.4: Replaces the previous pattern where the version was duplicated
// across:
//   - cmd/sentinel/version.go (var Version = "dev", set via -ldflags)
//   - chart/Chart.yaml (version: 3.8.0, appVersion: "3.8.0")
//   - chart/values.yaml (image.tag: "3.8.0")
//   - README.md (helm install ... --version 3.8.2)
//   - git tags (v3.8.3)
//
// The drift between these was a recurring source of release bugs. Now the
// VERSION file at the repo root is the canonical source. The build process
// reads it and sets the version via -ldflags; the Helm chart reads it via
// a Helm plugin or by having the release script sed-replace Chart.yaml;
// the README references {{VERSION}} which is substituted at release time.
//
// The Go code reads the version from the Version variable, which is set
// at build time. If unset (e.g. `go run`), it falls back to "dev".
package version

// Version is the canonical Sentinel version. Set at build time via
// -ldflags="-X github.com/karimzakzouk/sentinel/v3/internal/version.Version=3.8.4".
// Defaults to "dev" when unset (e.g. during `go run` or `go test`).
var Version = "dev"
