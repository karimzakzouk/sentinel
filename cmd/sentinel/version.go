package main

import "github.com/karimzakzouk/sentinel/v3/internal/version"

// v3.8.4: Version is now defined in internal/version, sourced from the
// VERSION file at the repo root via -ldflags at build time. The Makefile
// and the GitHub Actions release workflow both read VERSION and pass it
// through -ldflags. This file is kept for backward compatibility with
// code that still references main.Version — it just re-exports the
// package-level value.
//
// To bump the version: edit the VERSION file at the repo root, then
// rebuild. The Helm chart and README are updated by `make release-bump`.
var Version = version.Version
