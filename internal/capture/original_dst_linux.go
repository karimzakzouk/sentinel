//go:build linux

package capture

import (
	"encoding/binary"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

// originalDestination returns the pre-REDIRECT destination host:port.
func originalDestination(conn net.Conn) (hostPort string, err error) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return "", fmt.Errorf("original destination needs TCP")
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return "", err
	}
	var (
		ip   net.IP
		port int
		cerr error
	)
	err = raw.Control(func(fd uintptr) {
		var sa unix.RawSockaddrInet4
		size := uint32(unsafe.Sizeof(sa))
		_, _, errno := unix.Syscall6(
			unix.SYS_GETSOCKOPT,
			fd,
			unix.IPPROTO_IP,
			uintptr(unix.SO_ORIGINAL_DST),
			uintptr(unsafe.Pointer(&sa)),   //nolint:gosec // SO_ORIGINAL_DST sockaddr
			uintptr(unsafe.Pointer(&size)), //nolint:gosec // sockopt length
			0,
		)
		if errno != 0 {
			cerr = errno
			return
		}
		ip = net.IPv4(sa.Addr[0], sa.Addr[1], sa.Addr[2], sa.Addr[3])
		pb := (*[2]byte)(unsafe.Pointer(&sa.Port)) //nolint:gosec // network-order port
		port = int(binary.BigEndian.Uint16(pb[:]))
	})
	if err != nil {
		return "", err
	}
	if cerr != nil {
		return "", cerr
	}
	if ip == nil || port == 0 {
		return "", fmt.Errorf("empty original destination")
	}
	return net.JoinHostPort(ip.String(), fmt.Sprint(port)), nil
}
