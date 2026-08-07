// Package version exposes the Sentinel build version.
//
// The version is injected at build time via -ldflags:
//
//	go build -ldflags="-X github.com/karimzakzouk/sentinel/internal/version.Version=1.0.0"
//
// When unset (go run, go test), Version is "dev".
package version

// Version is the Sentinel build version. Set via ldflags; "dev" when unset.
var Version = "dev"
