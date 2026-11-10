//go:build !linux

package lab

import "errors"

// ApplySeal seals the network namespace this process runs in; only Linux
// has one.
func ApplySeal([]Allow) (int, error) {
	return 0, errors.New("the Lab's firewall runs only on Linux")
}
