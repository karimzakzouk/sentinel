// Package depanalysis analyzes source code dependencies to identify when
// an error originates from a shared library rather than the service itself.
// If 12 services fail from the same shared library bug, Sentinel should
// open ONE PR in the shared library, not 12 PRs in 12 repos.
// Approach:
//   1. Analyze the error's stack trace to find the source file
//   2. Check if that file is in a vendor/, node_modules/, or similar directory
//   3. If so, extract the dependency name (from go.mod, package.json, requirements.txt, etc.)
//   4. Look up the dependency's source repo (GitHub URL)
//   5. Return a SharedLibFix recommendation: fix in the shared lib repo, not the service
// Supported ecosystems:
//   - Go: vendor/ + go.mod → github.com/org/repo
//   - Python: site-packages/ + requirements.txt / pyproject.toml → PyPI package → GitHub
//   - Node.js: node_modules/ + package.json → npm package → GitHub
//   - Java: .jar + pom.xml → Maven coordinates → GitHub
//   - Ruby: gems/ + Gemfile → RubyGems → GitHub
package depanalysis

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// SharedLibFix is the result of dependency analysis.
// When an error originates from a shared library, the investigator
// uses this to redirect the fix to the library's repo instead of the service.

type SharedLibFix struct {
	IsSharedLib    bool   // true if the error is in a shared library
	DependencyName string // e.g. "github.com/org/lib", "requests", "lodash"
	SourceRepo     string // e.g. "github.com/org/lib" (for cloning)
	LibraryPath    string // path within the service repo (e.g. "vendor/github.com/org/lib")
	Language       string // "go", "python", "javascript", "java", "ruby", "unknown"
}

// AnalyzeStackTrace determines if the error's source file is in a shared
// library (vendor/, node_modules/, site-packages/, etc.) and extracts the
// dependency info.
// Called by the investigator after parsing the error. If IsSharedLib
// is true, the investigator should clone the dependency's repo and fix there.

func AnalyzeStackTrace(sourceFile, podLogs string) SharedLibFix {
	result := SharedLibFix{
		Language: detectLanguageFromPath(sourceFile),
	}

	if sourceFile == "" {
		return result
	}

	// Check if the file is in a known dependency directory

	depDir, depName := extractDependencyName(sourceFile)
	if depName == "" {
		return result
	}

	result.IsSharedLib = true
	result.DependencyName = depName
	result.LibraryPath = depDir
	result.SourceRepo = resolveDependencyRepo(depName, result.Language)

	return result
}

// extractDependencyName checks if a file path is inside a dependency directory
// and extracts the dependency name.
// Examples:
//   "vendor/github.com/org/lib/foo.go"       → "github.com/org/lib"
//   "node_modules/lodash/index.js"           → "lodash"
//   "node_modules/@scope/pkg/index.js"       → "@scope/pkg"
//   "site-packages/requests/__init__.py"     → "requests"
//   ".gem/gems/nokogiri-1.16/lib/nokogiri.rb" → "nokogiri"

func extractDependencyName(path string) (depDir, depName string) {
	// Normalize path

	path = filepath.ToSlash(path)

	// Go vendor/

	if strings.Contains(path, "vendor/") {
		parts := strings.Split(path, "vendor/")
		if len(parts) >= 2 {
			afterVendor := parts[len(parts)-1]
			// For Go: "github.com/org/lib/foo.go" → "github.com/org/lib"
			// The dependency is the first 3 segments (host/org/repo)

			segments := strings.Split(afterVendor, "/")
			if len(segments) >= 3 {
				depName = strings.Join(segments[:3], "/")
			} else if len(segments) > 0 {
				depName = segments[0]
			}
			depDir = "vendor/" + depName
			return
		}
	}

	// Node.js node_modules/

	if strings.Contains(path, "node_modules/") {
		parts := strings.Split(path, "node_modules/")
		if len(parts) >= 2 {
			afterNM := parts[len(parts)-1]
			// Handle scoped packages: @scope/pkg

			segments := strings.Split(afterNM, "/")
			if len(segments) >= 2 && strings.HasPrefix(segments[0], "@") {
				depName = segments[0] + "/" + segments[1]
			} else if len(segments) >= 1 {
				depName = segments[0]
			}
			depDir = "node_modules/" + depName
			return
		}
	}

	// Python site-packages/

	if strings.Contains(path, "site-packages/") {
		parts := strings.Split(path, "site-packages/")
		if len(parts) >= 2 {
			afterSP := parts[len(parts)-1]
			segments := strings.Split(afterSP, "/")
			if len(segments) >= 1 {
				depName = segments[0]
				// Strip .dist-info suffix

				depName = strings.Split(depName, "-")[0]
				depDir = "site-packages/" + depName
			}
			return
		}
	}

	// Ruby gems/

	if strings.Contains(path, "/gems/") {
		parts := strings.Split(path, "/gems/")
		if len(parts) >= 2 {
			afterGems := parts[len(parts)-1]
			segments := strings.Split(afterGems, "/")
			if len(segments) >= 1 {
				// Gem name is like "nokogiri-1.16.0"

				depName = strings.Split(segments[0], "-")[0]
				depDir = "gems/" + segments[0]
			}
			return
		}
	}

	// Java .jar files

	if strings.HasSuffix(path, ".jar") {
		// Extract the artifact name from the JAR path

		base := filepath.Base(path)
		depName = strings.TrimSuffix(base, ".jar")
		// Strip version: "commons-lang3-3.12.0" → "commons-lang3"

		depName = stripVersionSuffix(depName)
		depDir = path
		return
	}

	return "", ""
}

// resolveDependencyRepo maps a dependency name to its source repository.
// For Go modules, the dependency name IS the repo (github.com/org/repo).
// For other ecosystems, we'd need a registry lookup (PyPI, npm, etc.).
// For now, we handle Go directly and return empty for others
// (the investigator can fall back to a web search or manual configuration).

func resolveDependencyRepo(depName, language string) string {
	switch language {
	case "go":
		// Go module paths ARE repo paths: github.com/org/repo

		if strings.HasPrefix(depName, "github.com/") {
			return depName
		}
		// Other Git hosts: gitlab.com, bitbucket.org, etc.

		if strings.HasPrefix(depName, "gitlab.com/") ||
			strings.HasPrefix(depName, "bitbucket.org/") {
			return depName
		}
		// Private modules — can't resolve without goproxy

		return ""

	case "python":
		// Would need PyPI API lookup: https://pypi.org/pypi/<name>/json
		// Return empty for now — the investigator can search GitHub

		return ""

	case "javascript":
		// Would need npm registry lookup: https://registry.npmjs.org/<name>

		return ""

	case "ruby":
		// Would need RubyGems API: https://rubygems.org/api/v1/gems/<name>.json

		return ""

	case "java":
		// Would need Maven Central lookup

		return ""
	}

	return ""
}

// detectLanguageFromPath guesses the language from the file extension.

func detectLanguageFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".py":
		return "python"
	case ".go":
		return "go"
	case ".js", ".jsx", ".ts", ".tsx":
		return "javascript"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".rs":
		return "rust"
	case ".c", ".cpp", ".h":
		return "c"
	}

	// Check path patterns

	if strings.Contains(path, "site-packages") {
		return "python"
	}
	if strings.Contains(path, "node_modules") {
		return "javascript"
	}
	if strings.Contains(path, "vendor/github.com") {
		return "go"
	}
	if strings.Contains(path, "/gems/") {
		return "ruby"
	}

	return "unknown"
}

// versionSuffixRe matches version numbers at the end of a string.

var versionSuffixRe = regexp.MustCompile(`-\d+(\.\d+)*$`)

// stripVersionSuffix removes a version suffix from a package name.
// "commons-lang3-3.12.0" → "commons-lang3"
// "spring-core-5.3.20" → "spring-core"

func stripVersionSuffix(name string) string {
	return versionSuffixRe.ReplaceAllString(name, "")
}

// FormatSharedLibFix returns a human-readable description of the shared lib
// fix recommendation.Used in the PR body when Sentinel opens a fix
// in a shared library instead of the service.

func FormatSharedLibFix(slf SharedLibFix) string {
	if !slf.IsSharedLib {
		return "not a shared library fix"
	}
	repo := slf.SourceRepo
	if repo == "" {
		repo = "(unknown — manual lookup required)"
	}
	return fmt.Sprintf("shared library fix: dependency=%s language=%s repo=%s (path in service: %s)",
		slf.DependencyName, slf.Language, repo, slf.LibraryPath)
}
