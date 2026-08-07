// Package errs provides typed error sentinels used across Sentinel.
package errs

import (
	"errors"
	"strings"
)

// ErrAlreadyExists is returned when a K8s resource creation collides with an
// existing resource. Callers can wrap or compare via errors.Is.
var ErrAlreadyExists = errors.New("already exists")

// IsAlreadyExists reports whether err is or wraps ErrAlreadyExists, or carries
// the substring "already exists" (the convention used by the K8s API server).
func IsAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrAlreadyExists) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}
