// Package main — synchronized accessors for package-level mutable clients.
//
// The package-level vars (pkgGHExec, pkgChatopsGH, etc.) are read from
// multiple goroutines (investigation handler, chatops handler, auto-merger)
// and written from the GitHub App token-rotation goroutine. Without
// synchronization this is a data race.
//
// This file provides RWMutex-protected getters and setters. All access
// sites MUST use these — direct access to the vars is a data race.
//
// Why not atomic.Pointer? The vars are mutated via WithTokenProvider()
// which mutates the receiver in place (returns the same pointer). So we
// need a mutex around the mutation, not just an atomic swap.
package main

import (
        "sync"

        "github.com/karimzakzouk/sentinel/internal/automerge"
        "github.com/karimzakzouk/sentinel/internal/chatops"
        "github.com/karimzakzouk/sentinel/internal/config"
        "github.com/karimzakzouk/sentinel/internal/executors"
        "github.com/karimzakzouk/sentinel/internal/prreview"
)

// pkgMu protects all pkg* vars below. RWMutex because reads (during
// investigations) are frequent and writes (during token rotation) are rare.
var pkgMu sync.RWMutex

var (
        pkgGHExec         *executors.GitHubExecutor
        pkgChatopsGH      *chatops.GitHubClientImpl
        pkgAutoMergeGH    *automerge.GitHubClientImpl
        pkgPrReviewer     *prreview.Reviewer
        pkgCfg            *config.Config
        pkgAutoMergeMerger *automerge.Merger
)

// Get* functions return the current value under a read lock. Safe to call
// from any goroutine. The returned pointer may be mutated by the caller's
// own methods (e.g. WithTokenProvider) — but the caller should hold the
// write lock if mutating.

// Compile-time references to prevent "unused" lint warnings. These
// accessors exist for future call sites that need thread-safe reads of
// the package-level clients; they're not all called yet.
var (
        _ = getGHExec
        _ = getChatopsGH
        _ = getAutoMergeGH
        _ = getPrReviewer
        _ = getCfg
        _ = getAutoMergeMerger
        _ = withPkgWriteLock
)

func getGHExec() *executors.GitHubExecutor {
        pkgMu.RLock()
        defer pkgMu.RUnlock()
        return pkgGHExec
}

func getChatopsGH() *chatops.GitHubClientImpl {
        pkgMu.RLock()
        defer pkgMu.RUnlock()
        return pkgChatopsGH
}

func getAutoMergeGH() *automerge.GitHubClientImpl {
        pkgMu.RLock()
        defer pkgMu.RUnlock()
        return pkgAutoMergeGH
}

func getPrReviewer() *prreview.Reviewer {
        pkgMu.RLock()
        defer pkgMu.RUnlock()
        return pkgPrReviewer
}

func getCfg() *config.Config {
        pkgMu.RLock()
        defer pkgMu.RUnlock()
        return pkgCfg
}

func getAutoMergeMerger() *automerge.Merger {
        pkgMu.RLock()
        defer pkgMu.RUnlock()
        return pkgAutoMergeMerger
}

// withPkgWriteLock holds the write lock while fn runs. Use for mutations
// like WithTokenProvider() that change the client in place.
func withPkgWriteLock(fn func()) {
        pkgMu.Lock()
        defer pkgMu.Unlock()
        fn()
}
