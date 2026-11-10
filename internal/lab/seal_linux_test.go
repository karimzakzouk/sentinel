//go:build linux

package lab

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const sealChildEnv = "SENTINEL_LAB_SEAL_CHILD"

// The firewall, installed in a network namespace of its own, lets new
// connections go only to the destinations it names and over loopback:
// another address on the same network is refused at once, while the named
// one gets through the seal (and, with nothing there, goes unanswered).
func TestTheFirewallRefusesAllButItsDestinations(t *testing.T) {
	if os.Getenv(sealChildEnv) == "1" {
		sealChild(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTheFirewallRefusesAllButItsDestinations$", "-test.v")
	cmd.Env = append(os.Environ(), sealChildEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	switch {
	case err != nil && (errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EINVAL)):
		t.Skipf("no user and network namespace for the test: %v", err)
	case strings.Contains(string(out), "--- SKIP"):
		t.Skipf("the child skipped:\n%s", out)
	case err != nil:
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("in its own namespaces:\n%s", out)
}

func sealChild(t *testing.T) {
	up := func(name string) {
		t.Helper()
		sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = unix.Close(sock) }()
		ifr, err := unix.NewIfreq(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := unix.IoctlIfreq(sock, unix.SIOCGIFFLAGS, ifr); err != nil {
			t.Fatalf("read %s's flags: %v", name, err)
		}
		ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
		if err := unix.IoctlIfreq(sock, unix.SIOCSIFFLAGS, ifr); err != nil {
			t.Fatalf("bring %s up: %v", name, err)
		}
	}
	up("lo")
	tun, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no TUN device: %v", err)
	}
	defer func() { _ = tun.Close() }()
	ifr, err := unix.NewIfreq("seal0")
	if err != nil {
		t.Fatal(err)
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(int(tun.Fd()), unix.TUNSETIFF, ifr); err != nil {
		t.Skipf("cannot create a TUN interface: %v", err)
	}
	sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(sock) }()
	// The address first: a netmask needs one to apply to.
	for _, set := range []struct {
		req  uint
		addr string
	}{{unix.SIOCSIFADDR, "10.9.0.1"}, {unix.SIOCSIFNETMASK, "255.255.255.0"}} {
		ifr, _ := unix.NewIfreq("seal0")
		if err := ifr.SetInet4Addr(netip.MustParseAddr(set.addr).AsSlice()); err != nil {
			t.Fatal(err)
		}
		if err := unix.IoctlIfreq(sock, set.req, ifr); err != nil {
			t.Fatalf("address seal0: %v", err)
		}
	}
	up("seal0")

	allowed := netip.MustParseAddrPort("10.9.0.9:80")
	n, err := ApplySeal([]Allow{{Proto: "tcp", Addr: allowed}})
	if err != nil {
		if errors.Is(err, syscall.EPROTONOSUPPORT) || errors.Is(err, syscall.ENOENT) {
			t.Skipf("this kernel has no nftables: %v", err)
		}
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("the seal has %d rules, want 5: loopback, replies, the destination and two refusals", n)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		if c, err := l.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	c, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("loopback was refused: %v", err)
	}
	_ = c.Close()

	start := time.Now()
	_, err = net.DialTimeout("tcp", "10.9.0.8:80", 3*time.Second)
	if !errors.Is(err, syscall.ECONNREFUSED) || time.Since(start) > time.Second {
		t.Fatalf("a destination the seal does not name: %v after %s; want refused at once", err, time.Since(start))
	}
	_, err = net.DialTimeout("tcp", allowed.String(), 300*time.Millisecond)
	if err == nil || errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("the named destination: %v; want it let through, unanswered", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("the named destination: %v; want a timeout, as nothing is there", err)
	}
}
