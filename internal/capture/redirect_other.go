//go:build !linux

package capture

import "fmt"

// ApplyRedirect is only implemented on Linux.
func ApplyRedirect(listenPort uint16) error {
	return fmt.Errorf("capture MITM redirect needs Linux")
}

// ClearRedirect is a no-op off Linux.
func ClearRedirect() error { return nil }
