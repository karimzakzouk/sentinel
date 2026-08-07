package depanalysis

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// SharedLibFix is the result of dependency analysis. When IsSharedLib is
// true, the investigator should clone SourceRepo (if non-empty) and apply
// the fix there instead of the service repo.
type SharedLibFix struct {
	IsSharedLib    bool
	DependencyName string
	SourceRepo     string
	LibraryPath    string
	Language       string
}

// AnalyzeStackTrace determines whether sourceFile lives inside a dependency
// directory (vendor/, node_modules/, site-packages/, gems/, or a .jar) and,
// if so, extracts the dependency name and source repo. Returns a zero
// SharedLibFix with only Language populated when sourceFile is empty or
// belongs to the service's own code.
func AnalyzeStackTrace(sourceFile, podLogs string) SharedLibFix {
	result := SharedLibFix{
		Language: detectLanguageFromPath(sourceFile),
	}
	if sourceFile == "" {
		return result
	}
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

// extractDependencyName checks whether a file path is inside a dependency
// directory and extracts the dependency name.
//
// Examples:
//
//	"vendor/github.com/org/lib/foo.go"          → "vendor/github.com/org/lib", "github.com/org/lib"
//	"node_modules/lodash/index.js"               → "node_modules/lodash", "lodash"
//	"node_modules/@scope/pkg/index.js"           → "node_modules/@scope/pkg", "@scope/pkg"
//	"site-packages/requests/__init__.py"         → "site-packages/requests", "requests"
//	"/var/lib/gems/2.7.0/gems/nokogiri-1.16/lib" → "gems/nokogiri-1.16", "nokogiri"
func extractDependencyName(path string) (depDir, depName string) {
	path = filepath.ToSlash(path)

	if strings.Contains(path, "vendor/") {
		parts := strings.Split(path, "vendor/")
		if len(parts) >= 2 {
			afterVendor := parts[len(parts)-1]
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

	if strings.Contains(path, "node_modules/") {
		parts := strings.Split(path, "node_modules/")
		if len(parts) >= 2 {
			afterNM := parts[len(parts)-1]
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

	if strings.Contains(path, "site-packages/") {
		parts := strings.Split(path, "site-packages/")
		if len(parts) >= 2 {
			afterSP := parts[len(parts)-1]
			segments := strings.Split(afterSP, "/")
			if len(segments) >= 1 {
				depName = strings.Split(segments[0], "-")[0]
				depDir = "site-packages/" + depName
			}
			return
		}
	}

	if strings.Contains(path, "/gems/") {
		parts := strings.Split(path, "/gems/")
		if len(parts) >= 2 {
			afterGems := parts[len(parts)-1]
			segments := strings.Split(afterGems, "/")
			if len(segments) >= 1 {
				depName = strings.Split(segments[0], "-")[0]
				depDir = "gems/" + segments[0]
			}
			return
		}
	}

	if strings.HasSuffix(path, ".jar") {
		base := filepath.Base(path)
		depName = strings.TrimSuffix(base, ".jar")
		depName = stripVersionSuffix(depName)
		depDir = path
		return
	}

	return "", ""
}

// resolveDependencyRepo maps a dependency name to its source repository. For
// Go modules the dependency name IS the repo (github.com/org/repo). For
// other ecosystems, the empty string is returned — the investigator can
// fall back to a web search or operator-supplied mapping.
func resolveDependencyRepo(depName, language string) string {
	switch language {
	case "go":
		if strings.HasPrefix(depName, "github.com/") {
			return depName
		}
		if strings.HasPrefix(depName, "gitlab.com/") ||
			strings.HasPrefix(depName, "bitbucket.org/") {
			return depName
		}
		return ""
	case "python", "javascript", "ruby", "java":
		return ""
	}
	return ""
}

// detectLanguageFromPath guesses the language from the file extension or,
// failing that, from the dependency-directory pattern in the path.
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
	case ".jar":
		return "java"
	}

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

// versionSuffixRe matches a trailing -N.N.N version on a package name.
var versionSuffixRe = regexp.MustCompile(`-\d+(\.\d+)*$`)

// stripVersionSuffix removes a trailing version from a package name:
// "commons-lang3-3.12.0" → "commons-lang3", "spring-core-5.3.20" → "spring-core".
func stripVersionSuffix(name string) string {
	return versionSuffixRe.ReplaceAllString(name, "")
}

// FormatSharedLibFix returns a human-readable description of the
// recommendation, included in the PR body when Sentinel opens a fix in a
// shared library instead of the service repo.
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
