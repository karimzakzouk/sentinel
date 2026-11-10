package capture

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCaptureMITMForwardsAndPostsBodies(t *testing.T) {
	ca, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "ca.crt")
	keyFile := filepath.Join(dir, "ca.key")
	if err := os.WriteFile(certFile, ca.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, ca.KeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}

	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"pay":1}` {
			t.Errorf("origin body %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer origin.Close()

	var gotBody []byte
	var mu sync.Mutex
	otlp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = b
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer otlp.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := ln.Addr().String()
	_ = ln.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- ServeMITM(MITMConfig{
			Listen:     listen,
			CACertFile: certFile,
			CAKeyFile:  keyFile,
			OTLP:       otlp.URL + "/v1/traces",
			Token:      "tok",
			Resolve:    func(string) (string, string, string, bool) { return "shop", "Deployment", "cart", true },
			DialTLS: func(network, _ string) (net.Conn, error) {
				return tls.Dial(network, strings.TrimPrefix(origin.URL, "https://"), &tls.Config{InsecureSkipVerify: true})
			},
			HTTP: otlp.Client(),
		})
	}()
	waitTCP(t, listen)

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("capture CA PEM")
	}
	proxy, err := url.Parse("http://" + listen)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxy),
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12, ServerName: "origin.test"},
		},
	}
	req, err := http.NewRequest(http.MethodPost, "https://origin.test/checkout", strings.NewReader(`{"pay":1}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	out, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(out) != `{"ok":true}` {
		t.Fatalf("response %d %s", res.StatusCode, out)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		b := append([]byte(nil), gotBody...)
		mu.Unlock()
		if len(b) > 0 {
			var payload map[string]any
			if err := json.Unmarshal(b, &payload); err != nil {
				t.Fatal(err)
			}
			s := string(b)
			if !strings.Contains(s, "http.request.body.content") || !strings.Contains(s, `"shop"`) || !strings.Contains(s, `"cart"`) {
				t.Fatalf("OTLP payload %s", s)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no OTLP post")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not listening on %s", addr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
