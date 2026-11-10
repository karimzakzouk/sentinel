package lab

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTheLabCAIssuesAHostCertificateCopiesTrust(t *testing.T) {
	ca, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := ca.Issue("api.stripe.com")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("host key pair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("Lab CA PEM did not parse")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "api.stripe.com", Roots: pool}); err != nil {
		t.Fatalf("copy with Lab CA trust should verify the host cert: %v", err)
	}
	other, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	otherPool := x509.NewCertPool()
	otherPool.AppendCertsFromPEM(other.CertPEM())
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "api.stripe.com", Roots: otherPool}); err == nil {
		t.Fatal("a different Lab CA must not verify the host cert")
	}
}

func TestAnHTTPSFakeAnswersRecordedStatusesWithALabCertificate(t *testing.T) {
	ca, err := NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := ca.Issue("api.stripe.com")
	if err != nil {
		t.Fatal(err)
	}
	routes := []fakeRoute{
		{Method: http.MethodGet, Path: "/v1/balance", Status: 200},
		{Method: http.MethodPost, Path: "/v1/charges", Status: 402},
	}
	srv := httptest.NewUnstartedServer(fakeHTTPHandler(routes))
	srv.TLS = &tls.Config{Certificates: mustKeyPair(t, certPEM, keyPEM), MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.CertPEM())
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    pool,
		ServerName: "api.stripe.com",
		MinVersion: tls.VersionTLS12,
	}}}
	ask := func(method, path string) int {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if got := ask(http.MethodGet, "/v1/balance"); got != 200 {
		t.Fatalf("GET /v1/balance -> %d", got)
	}
	if got := ask(http.MethodPost, "/v1/charges"); got != 402 {
		t.Fatalf("POST /v1/charges -> %d", got)
	}
	if got := ask(http.MethodGet, "/missing"); got != 404 {
		t.Fatalf("unmatched -> %d", got)
	}
}

func mustKeyPair(t *testing.T, certPEM, keyPEM []byte) []tls.Certificate {
	t.Helper()
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return []tls.Certificate{c}
}
