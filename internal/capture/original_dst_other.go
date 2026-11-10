//go:build !linux

package capture

import (
	"fmt"
	"net"
)

func originalDestination(conn net.Conn) (string, error) {
	return "", fmt.Errorf("original destination needs Linux")
}
