package lab

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/karimzakzouk/sentinel/internal/capture"
)

func TestMITMProxyTerminatesTLSAndReturnsRecordedBodies(t *testing.T) {
	ca, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	byHost := groupMITMRoutes([]mitmRoute{
		{Host: "api.stripe.com", Method: http.MethodGet, Path: "/v1/balance", Status: 200, Body: `{"available":[]}`},
		{Host: "api.stripe.com", Method: http.MethodPost, Path: "/v1/charges", Status: 402, Body: `{"error":"card"}`},
		{Host: "hooks.example", Method: http.MethodGet, Path: "/health", Status: 204},
	})
	proxy := httptest.NewServer(mitmHandler(ca, byHost))
	t.Cleanup(proxy.Close)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM())
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		},
	}}

	res, err := client.Get("https://api.stripe.com/v1/balance")
	if err != nil {
		t.Fatalf("GET through MITM: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || string(body) != `{"available":[]}` {
		t.Fatalf("GET balance: %d %q", res.StatusCode, body)
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.stripe.com/v1/charges", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST through MITM: %v", err)
	}
	body, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 402 || string(body) != `{"error":"card"}` {
		t.Fatalf("POST charges: %d %q", res.StatusCode, body)
	}

	res, err = client.Get("https://hooks.example/health")
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatalf("GET health: %d", res.StatusCode)
	}

	res, err = client.Get("https://api.stripe.com/missing")
	if err != nil {
		t.Fatalf("GET missing: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("unmatched path: %d", res.StatusCode)
	}
}

func TestMITMNeverNeedsAnOutboundDial(t *testing.T) {
	// A host with no routes still completes CONNECT+TLS and answers 404; the
	// proxy must not dial the real name (sealed Lab).
	ca, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(mitmHandler(ca, map[string][]fakeRoute{}))
	t.Cleanup(proxy.Close)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM())
	proxyURL, _ := url.Parse(proxy.URL)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DialContext:     (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext,
	}}
	res, err := client.Get("https://only.inside.the.lab/x")
	if err != nil {
		t.Fatalf("MITM must answer without dialling outside: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatalf("empty catalog: %d", res.StatusCode)
	}
}

func TestServeMITMLoadsCAAndRoutesFromFiles(t *testing.T) {
	ca, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caCert := filepath.Join(dir, "ca.crt")
	caKey := filepath.Join(dir, "ca.key")
	routesPath := filepath.Join(dir, "routes")
	if err := os.WriteFile(caCert, ca.CertPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caKey, ca.KeyPEM(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(routesPath, []byte(mitmRoutesFile([]mitmRoute{
		{Host: "api.x", Method: http.MethodGet, Path: "/ok", Status: 200, Body: "yes"},
	})), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	done := make(chan error, 1)
	go func() { done <- ServeMITM(addr, caCert, caKey, routesPath) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ServeMITM did not listen: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM())
	proxyURL, _ := url.Parse("http://" + addr)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}
	res, err := client.Get("https://api.x/ok")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || string(body) != "yes" {
		t.Fatalf("got %d %q", res.StatusCode, body)
	}
}

func TestHTTPSWorldGetsAMITMAndRewritesProxyEnv(t *testing.T) {
	w := shop()
	front := w.Workloads[ref("Deployment", "front")]
	front.Template.Spec.Containers[0].Env = append(front.Template.Spec.Containers[0].Env,
		corev1.EnvVar{Name: "HTTPS_PROXY", Value: "http://corp-proxy.invalid:3128"},
		corev1.EnvVar{Name: "HTTP_PROXY", Value: "http://corp-proxy.invalid:3128"},
	)
	w.Workloads[ref("Deployment", "front")] = front
	w.Calls = []capture.Call{
		{Host: "api.stripe.com", Method: "GET", Path: "/v1/balance", Port: 443, Status: 200, ResponseBody: `{"ok":1}`},
	}
	plan, err := Synthesize(w, ref("Deployment", "front"), "sentinel-lab-0")
	if err != nil {
		t.Fatal(err)
	}
	var mitmPod *corev1.Pod
	var mitmSvc *corev1.Service
	var mitmCM *corev1.ConfigMap
	for _, p := range plan.Pods {
		if p.Annotations[FakeMITM] == "true" {
			mitmPod = p
		}
	}
	for _, s := range plan.Services {
		if s.Name == MITMName {
			mitmSvc = s
		}
	}
	for _, c := range plan.ConfigMaps {
		if c.Name == MITMName {
			mitmCM = c
		}
	}
	if mitmPod == nil || mitmSvc == nil || mitmCM == nil {
		t.Fatalf("Lab MITM missing: pod=%v svc=%v cm=%v", mitmPod != nil, mitmSvc != nil, mitmCM != nil)
	}
	if mitmSvc.Spec.Ports[0].Port != MITMPort {
		t.Fatalf("MITM port %d", mitmSvc.Spec.Ports[0].Port)
	}
	if !strings.Contains(mitmCM.Data["routes"], "api.stripe.com") || !strings.Contains(mitmCM.Data["routes"], "/v1/balance") {
		t.Fatalf("MITM routes %q", mitmCM.Data["routes"])
	}
	copyPod := plan.Pods[0]
	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY"} {
		if !slices.ContainsFunc(copyPod.Spec.Containers[0].Env, func(e corev1.EnvVar) bool {
			return e.Name == name && e.Value == MITMProxyURL
		}) {
			t.Fatalf("copy %s not rewritten to Lab MITM: %+v", name, copyPod.Spec.Containers[0].Env)
		}
	}
	for _, want := range []string{
		"Lab MITM " + MITMProxyURL + " terminates TLS with the Lab CA for HTTPS_PROXY clients and answers recorded bodies without leaving the seal; direct DNS+trust to per-host fakes stays the default",
		"Deployment/shop/front's HTTP(S)_PROXY now points at the Lab MITM (" + MITMProxyURL + "), which terminates TLS with the Lab CA and answers recorded bodies without leaving the seal",
	} {
		if !slices.Contains(plan.Fidelity, want) {
			t.Fatalf("fidelity lacks %q:\n%s", want, strings.Join(plan.Fidelity, "\n"))
		}
	}
	got := withFakeTLSImage(mitmPod.DeepCopy(), "sentinel:dev")
	if got.Spec.Containers[0].Image != "sentinel:dev" {
		t.Fatalf("SealImage not applied to MITM: %s", got.Spec.Containers[0].Image)
	}
}

func TestMITMRoutesRoundTripBodies(t *testing.T) {
	in := []mitmRoute{{Host: "api.x", Method: "POST", Path: "/v1", Status: 201, Body: `{"id":"1"}`}}
	got := parseMITMRoutes(mitmRoutesFile(in))
	if len(got) != 1 || got[0].Host != "api.x" || got[0].Body != `{"id":"1"}` || got[0].Status != 201 {
		t.Fatalf("routes %+v", got)
	}
	fromCalls := mitmRoutesFromCalls([]capture.Call{
		{Host: "api.x", Method: "POST", Path: "/v1", Port: 443, Status: 201, ResponseBody: "hi"},
		{Host: "api.x", Method: "GET", Path: "/v1", Port: 80, Status: 200, ResponseBody: "skip"},
		{Host: "api.x", Method: "gRPC", Path: "/pkg/M", Port: 443, Status: 0},
	}, map[string]bool{"api.x": true})
	if len(fromCalls) != 1 || fromCalls[0].Body != "hi" {
		t.Fatalf("from calls %+v", fromCalls)
	}
}

func TestAuthorityFromPEMRoundTrip(t *testing.T) {
	ca, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := AuthorityFromPEM(ca.CertPEM(), ca.KeyPEM())
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := loaded.Issue("pay.example")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM())
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "pay.example", Roots: pool}); err != nil {
		t.Fatal(err)
	}
}
