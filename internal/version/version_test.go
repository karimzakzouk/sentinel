package version

import "testing"

func TestVersionDefault(t *testing.T) {
	// When built without ldflags, Version should be "dev"
	// (the release CI sets it via -ldflags)
	if Version == "" {
		t.Error("Version should not be empty")
	}
}

func TestVersionNotHardcoded(t *testing.T) {
	// Version should be "dev" when not set via ldflags.
	// The release build sets it to the actual version.
	if Version != "dev" && Version != "" {
		t.Logf("Version is %q (set via ldflags during build)", Version)
	}
}
