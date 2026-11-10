package lab

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMITMPodMountsTheLabCA(t *testing.T) {
	f := mitmOf("lab-0")
	if f.Pod.Name != MITMName || f.Service.Name != MITMName {
		t.Fatalf("names %+v %+v", f.Pod.Name, f.Service.Name)
	}
	c := f.Pod.Spec.Containers[0]
	if c.Name != "mitm" || len(c.VolumeMounts) == 0 || c.VolumeMounts[0].MountPath != "/etc/lab-ca" {
		t.Fatalf("container %+v", c)
	}
	if got := f.Service.Spec.Ports[0].Port; got != int32(MITMPort) {
		t.Fatalf("port %d", got)
	}
}

func TestServeMITMRejectsNonCONNECT(t *testing.T) {
	ca, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caCert := filepath.Join(dir, "ca.crt")
	caKey := filepath.Join(dir, "ca.key")
	if err := os.WriteFile(caCert, ca.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caKey, ca.KeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	errc := make(chan error, 1)
	go func() { errc <- ServeMITM(addr, caCert, caKey) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	res, err := http.Get("http://" + addr + "/") //nolint:gosec // local test listener
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", res.StatusCode)
	}
}
