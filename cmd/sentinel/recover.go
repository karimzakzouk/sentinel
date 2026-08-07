// Package main — goroutine panic recovery helper.
//
// recoverPanic is the standard panic-recovery wrapper for every goroutine
// spawned in cmd/sentinel. A goroutine that panics without recovery kills
// the entire Sentinel process — this helper logs the panic + stack trace
// and lets the process keep running.
//
// Usage:
//
//	go func() {
//	    defer recoverPanic("sandbox reaper")
//	    // ... goroutine body ...
//	}()
package main

import (
	"runtime/debug"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// recoverPanic logs a panic + stack trace and recovers. Intended to be
// called as `defer recoverPanic("name")` at the top of every goroutine.
func recoverPanic(name string) {
	if r := recover(); r != nil {
		logging.Error("goroutine panic recovered",
			"goroutine", name,
			"panic", r,
			"stack", string(debug.Stack()),
		)
	}
}
