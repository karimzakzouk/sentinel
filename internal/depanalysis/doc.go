// Package depanalysis inspects source-file paths extracted from error stack
// traces to determine whether a failure originated in a shared library or in
// the service's own code.
//
// When 12 services fail with the same shared-library bug, Sentinel should
// open ONE PR in the shared library, not 12 PRs across 12 service repos. The
// investigator calls AnalyzeStackTrace with the top source-file path parsed
// from the pod logs; if the path is inside vendor/, node_modules/,
// site-packages/, gems/, or a .jar, the returned SharedLibFix carries the
// dependency name and (for Go modules) the source repo to clone.
//
// Supported ecosystems:
//
//   - Go: vendor/ → github.com/org/repo
//   - Python: site-packages/ → PyPI package (name only; registry lookup
//     is not bundled — caller falls back to a web search)
//   - Node.js: node_modules/ → npm package (name only)
//   - Ruby: gems/ → RubyGems name (strips -version suffix)
//   - Java: .jar → artifact name (strips -version suffix)
package depanalysis
