// Package version reports build information for Sentinel binaries.
package version

import (
	"runtime"
	"runtime/debug"
)

// Version is overridden at build time with
// -ldflags "-X github.com/karimzakzouk/sentinel/internal/version.Version=v0.1.0".
var Version = "dev"

// Info describes the running binary.
type Info struct {
	Version   string `json:"version"`
	Revision  string `json:"revision,omitempty"`
	Modified  bool   `json:"modified,omitempty"`
	GoVersion string `json:"goVersion"`
}

// Get combines the linker-provided version with the VCS stamp Go embeds at build time.
func Get() Info {
	info := Info{Version: Version, GoVersion: runtime.Version()}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			info.Revision = s.Value
		case "vcs.modified":
			info.Modified = s.Value == "true"
		}
	}
	return info
}

// UserAgent identifies Sentinel in Kubernetes API audit logs.
func UserAgent(component string) string {
	return "sentinel-" + component + "/" + Version
}
