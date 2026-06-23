package depanalysis

import (
	"testing"
)

// TestAnalyzeStackTrace_GoVendor verifies that a Go error in vendor/ is
// detected as a shared library dependency.
func TestAnalyzeStackTrace_GoVendor(t *testing.T) {
	result := AnalyzeStackTrace("vendor/github.com/acme/shared-lib/client.go", "")

	if !result.IsSharedLib {
		t.Error("expected IsSharedLib=true for vendor/ path")
	}
	if result.DependencyName != "github.com/acme/shared-lib" {
		t.Errorf("DependencyName = %q, want github.com/acme/shared-lib", result.DependencyName)
	}
	if result.SourceRepo != "github.com/acme/shared-lib" {
		t.Errorf("SourceRepo = %q, want github.com/acme/shared-lib", result.SourceRepo)
	}
	if result.Language != "go" {
		t.Errorf("Language = %q, want go", result.Language)
	}
}

// TestAnalyzeStackTrace_NodeModules verifies Node.js dependency detection.
func TestAnalyzeStackTrace_NodeModules(t *testing.T) {
	result := AnalyzeStackTrace("node_modules/lodash/lodash.js", "")

	if !result.IsSharedLib {
		t.Error("expected IsSharedLib=true for node_modules/ path")
	}
	if result.DependencyName != "lodash" {
		t.Errorf("DependencyName = %q, want lodash", result.DependencyName)
	}
	if result.Language != "javascript" {
		t.Errorf("Language = %q, want javascript", result.Language)
	}
}

// TestAnalyzeStackTrace_ScopedNpmPackage verifies @scope/pkg detection.
func TestAnalyzeStackTrace_ScopedNpmPackage(t *testing.T) {
	result := AnalyzeStackTrace("node_modules/@babel/core/lib/index.js", "")

	if !result.IsSharedLib {
		t.Error("expected IsSharedLib=true")
	}
	if result.DependencyName != "@babel/core" {
		t.Errorf("DependencyName = %q, want @babel/core", result.DependencyName)
	}
}

// TestAnalyzeStackTrace_PythonSitePackages verifies Python dependency detection.
func TestAnalyzeStackTrace_PythonSitePackages(t *testing.T) {
	result := AnalyzeStackTrace("/usr/local/lib/python3.11/site-packages/requests/models.py", "")

	if !result.IsSharedLib {
		t.Error("expected IsSharedLib=true for site-packages/ path")
	}
	if result.DependencyName != "requests" {
		t.Errorf("DependencyName = %q, want requests", result.DependencyName)
	}
	if result.Language != "python" {
		t.Errorf("Language = %q, want python", result.Language)
	}
}

// TestAnalyzeStackTrace_RubyGems verifies Ruby gem detection.
func TestAnalyzeStackTrace_RubyGems(t *testing.T) {
	result := AnalyzeStackTrace("/var/lib/gems/2.7.0/gems/nokogiri-1.16.0/lib/nokogiri/xml/node.rb", "")

	if !result.IsSharedLib {
		t.Error("expected IsSharedLib=true for gems/ path")
	}
	if result.DependencyName != "nokogiri" {
		t.Errorf("DependencyName = %q, want nokogiri", result.DependencyName)
	}
	if result.Language != "ruby" {
		t.Errorf("Language = %q, want ruby", result.Language)
	}
}

// TestAnalyzeStackTrace_NotSharedLib verifies that service code (not in a
// dependency directory) is NOT flagged as a shared library.
func TestAnalyzeStackTrace_NotSharedLib(t *testing.T) {
	result := AnalyzeStackTrace("app/main.py", "")

	if result.IsSharedLib {
		t.Error("expected IsSharedLib=false for service code")
	}
	if result.DependencyName != "" {
		t.Errorf("DependencyName = %q, want empty", result.DependencyName)
	}
}

// TestAnalyzeStackTrace_EmptyFile verifies empty input returns zero value.
func TestAnalyzeStackTrace_EmptyFile(t *testing.T) {
	result := AnalyzeStackTrace("", "")

	if result.IsSharedLib {
		t.Error("expected IsSharedLib=false for empty file")
	}
}

// TestStripVersionSuffix verifies version stripping from package names.
func TestStripVersionSuffix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"commons-lang3-3.12.0", "commons-lang3"},
		{"spring-core-5.3.20", "spring-core"},
		{"junit-4.13.2", "junit"},
		{"nokogiri", "nokogiri"},             // no version
		{"lib-1", "lib"},                      // single digit version
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := stripVersionSuffix(c.in)
			if got != c.want {
				t.Errorf("stripVersionSuffix(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestDetectLanguageFromPath verifies language detection from file paths.
func TestDetectLanguageFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"app/main.py", "python"},
		{"src/handler.go", "go"},
		{"lib/index.js", "javascript"},
		{"src/App.tsx", "javascript"},
		{"src/Main.java", "java"},
		{"app/server.rb", "ruby"},
		{"src/main.rs", "rust"},
		{"src/parser.c", "c"},
		{"src/wrapper.cpp", "c"},
		{"unknown.txt", "unknown"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := detectLanguageFromPath(c.path)
			if got != c.want {
				t.Errorf("detectLanguageFromPath(%q) = %q, want %q", c.path, got, c.want)
			}
		})
	}
}

// TestFormatSharedLibFix verifies the human-readable output.
func TestFormatSharedLibFix(t *testing.T) {
	slf := SharedLibFix{
		IsSharedLib:    true,
		DependencyName: "github.com/acme/lib",
		SourceRepo:     "github.com/acme/lib",
		LibraryPath:    "vendor/github.com/acme/lib",
		Language:       "go",
	}
	out := FormatSharedLibFix(slf)
	if out == "not a shared library fix" {
		t.Error("expected shared lib description, got default")
	}
}
