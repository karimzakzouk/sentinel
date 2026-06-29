package main

import "github.com/karimzakzouk/sentinel/internal/version"

// Version is the Sentinel binary version, sourced from the VERSION file at
// the repo root via -ldflags at build time. The Makefile and the GitHub
// Actions release workflow both read VERSION and pass it through -ldflags.
//
// To bump the version: edit VERSION, run `python3 scripts/sync-version.py`,
// commit, tag, push. CI verifies everything stays in sync.
var Version = version.Version
