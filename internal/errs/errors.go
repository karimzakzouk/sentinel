// Package errs defines sentinel errors used across Sentinel for typed
// error classification. Replaces string-matching patterns like
// `strings.Contains(err.Error(), "already exists")` with
// `errors.Is(err, errs.ErrAlreadyExists)`.
package errs

import (
	"errors"
	"fmt"
)

// K8s / resource errors
var (
	// ErrAlreadyExists is returned when a K8s resource already exists.
	// Non-fatal — the caller should treat this as idempotent success.
	ErrAlreadyExists = errors.New("already exists")

	// ErrNotFound is returned when a K8s resource is not found.
	ErrNotFound = errors.New("not found")
)

// Sandbox errors
var (
	// ErrSandboxInfra indicates a sandbox infrastructure failure (RBAC
	// denied, image pull failure, namespace creation failed).
	ErrSandboxInfra = errors.New("sandbox infrastructure error")
)

// GitHub errors
var (
	// ErrGitHubRateLimit indicates the GitHub API returned a rate-limit.
	ErrGitHubRateLimit = errors.New("github rate limited")

	// ErrGitHubUnprocessable indicates HTTP 422.
	ErrGitHubUnprocessable = errors.New("github unprocessable entity")
)

// Wrap wraps an error with a sentinel for classification via errors.Is.
func Wrap(err error, sentinel error) error {
	return fmt.Errorf("%w: %v", sentinel, err)
}

// IsAlreadyExists returns true if err represents an "already exists" condition.
func IsAlreadyExists(err error) bool {
	return errors.Is(err, ErrAlreadyExists) ||
		(err != nil && contains(err.Error(), "already exists"))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsCI(s, substr)
}

func containsCI(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	ls := []byte(s)
	lsub := []byte(substr)
	for i := 0; i <= len(ls)-len(lsub); i++ {
		match := true
		for j := 0; j < len(lsub); j++ {
			c1 := ls[i+j]
			c2 := lsub[j]
			if c1 >= 'A' && c1 <= 'Z' {
				c1 += 32
			}
			if c2 >= 'A' && c2 <= 'Z' {
				c2 += 32
			}
			if c1 != c2 {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
