package capture

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// MITMConfig runs a production HTTPS CONNECT proxy that terminates TLS with
// the capture CA, dials the real origin, and posts request/response bodies
// as OTLP JSON to Sentinel's capture receiver. Separate from the Lab MITM,
// which never dials out.
type MITMConfig struct {
	Listen     string
	CACertFile string
	CAKeyFile  string
	// OTLP is the traces URL, e.g. http://sentinel-capture:4318/v1/traces.
	OTLP string
	// Token is the bearer the capture receiver expects.
	Token string
	// Resolve maps the client IP (host part) to k8s workload identity.
	// When it returns false the exchange is still proxied but not posted.
	Resolve func(clientIP string) (namespace, kind, name string, ok bool)
	// DialTLS dials the origin; nil uses tls.Dial with system roots.
	DialTLS func(network, addr string) (net.Conn, error)
	// ClientTLS optionally returns a client certificate for the peer pod IP
	// when Settings capture.mitmClientTLS is on.
	ClientTLS func(clientIP string) *tls.Certificate
	HTTP      *http.Client
}

// ServeMITM listens until the listener fails.
func ServeMITM(cfg MITMConfig) error {
	if cfg.Listen == "" {
		return fmt.Errorf("capture-mitm needs --listen")
	}
	if _, err := strconv.Atoi(cfg.Listen); err == nil {
		cfg.Listen = ":" + cfg.Listen
	}
	certPEM, err := os.ReadFile(cfg.CACertFile)
	if err != nil {
		return fmt.Errorf("read capture CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(cfg.CAKeyFile)
	if err != nil {
		return fmt.Errorf("read capture CA key: %w", err)
	}
	ca, err := AuthorityFromPEM(certPEM, keyPEM)
	if err != nil {
		return err
	}
	if cfg.DialTLS == nil {
		cfg.DialTLS = func(network, addr string) (net.Conn, error) {
			return tls.Dial(network, addr, &tls.Config{MinVersion: tls.VersionTLS12})
		}
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	srv := &http.Server{
		Handler:           mitmHandler(ca, cfg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.Serve(ln)
}

func mitmHandler(ca *Authority, cfg MITMConfig) http.Handler {
	certs := &certCache{ca: ca}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		handleCaptureConnect(w, r, certs, cfg)
	})
}

func handleCaptureConnect(w http.ResponseWriter, r *http.Request, certs *certCache, cfg MITMConfig) {
	hostPort := r.Host
	host := hostPort
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		host = h
	} else {
		hostPort = net.JoinHostPort(host, "443")
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		http.Error(w, "CONNECT needs a host", http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "CONNECT hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, bufrw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = client.Close() }()
	if _, err := bufrw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = bufrw.Flush()
	raw := client
	if bufrw.Reader.Buffered() > 0 {
		raw = &bufConn{Conn: client, r: bufrw.Reader}
	}
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	tlsClient := tls.Server(raw, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := host
			if chi.ServerName != "" {
				name = strings.ToLower(chi.ServerName)
			}
			return certs.certificate(name)
		},
	})
	if err := tlsClient.Handshake(); err != nil {
		return
	}
	defer func() { _ = tlsClient.Close() }()
	upstream, err := dialOrigin(cfg, clientIP, hostPort)
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()
	proxyCapture(tlsClient, upstream, host, clientIP, cfg)
}

func dialOrigin(cfg MITMConfig, clientIP, addr string) (net.Conn, error) {
	dial := cfg.DialTLS
	if dial == nil {
		dial = func(network, a string) (net.Conn, error) {
			return tls.Dial(network, a, &tls.Config{MinVersion: tls.VersionTLS12})
		}
	}
	if cfg.ClientTLS == nil {
		return dial("tcp", addr)
	}
	cert := cfg.ClientTLS(clientIP)
	if cert == nil {
		return dial("tcp", addr)
	}
	return tls.Dial("tcp", addr, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{*cert},
	})
}

func proxyCapture(client, upstream net.Conn, host, clientIP string, cfg MITMConfig) {
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	_ = upstream.SetDeadline(time.Now().Add(30 * time.Second))
	req, err := http.ReadRequest(bufio.NewReader(client))
	if err != nil {
		return
	}
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(io.LimitReader(req.Body, bodyMax+1))
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		req.ContentLength = int64(len(reqBody))
	}
	if err := req.Write(upstream); err != nil {
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(upstream), req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, bodyMax+1))
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	resp.ContentLength = int64(len(respBody))
	if err := resp.Write(client); err != nil {
		return
	}
	postOTLP(cfg, host, clientIP, req, reqBody, resp.StatusCode, respBody)
}

func postOTLP(cfg MITMConfig, host, clientIP string, req *http.Request, reqBody []byte, status int, respBody []byte) {
	if cfg.OTLP == "" || cfg.Token == "" {
		return
	}
	ns, kind, name := "", "Deployment", ""
	if cfg.Resolve != nil {
		if n, k, w, ok := cfg.Resolve(clientIP); ok {
			ns, kind, name = n, k, w
		}
	}
	if ns == "" || name == "" {
		// Headers let a sidecar name itself without an IP lookup.
		ns = firstNonEmpty(req.Header.Get("X-Sentinel-Namespace"), ns)
		name = firstNonEmpty(req.Header.Get("X-Sentinel-Workload"), name)
		if k := req.Header.Get("X-Sentinel-Kind"); k != "" {
			kind = k
		}
	}
	if ns == "" || name == "" {
		return
	}
	path := req.URL.Path
	if path == "" {
		path = "/"
	}
	start := time.Now().UTC()
	end := start.Add(time.Millisecond)
	workloadAttr := "k8s.deployment.name"
	switch kind {
	case "StatefulSet":
		workloadAttr = "k8s.statefulset.name"
	case "DaemonSet":
		workloadAttr = "k8s.daemonset.name"
	}
	str := func(s string) map[string]any { return map[string]any{"stringValue": s} }
	attrs := []map[string]any{
		{"key": "http.request.method", "value": str(req.Method)},
		{"key": "url.path", "value": str(path)},
		{"key": "url.full", "value": str("https://" + host + path)},
		{"key": "http.response.status_code", "value": map[string]any{"intValue": strconv.Itoa(status)}},
	}
	if len(reqBody) > 0 && req.Method != http.MethodGet && req.Method != http.MethodHead {
		attrs = append(attrs, map[string]any{"key": "http.request.body.content", "value": str(string(clipBody(reqBody)))})
	}
	if len(respBody) > 0 {
		attrs = append(attrs, map[string]any{"key": "http.response.body.content", "value": str(string(clipBody(respBody)))})
	}
	payload := map[string]any{
		"resourceSpans": []map[string]any{{
			"resource": map[string]any{"attributes": []map[string]any{
				{"key": "k8s.namespace.name", "value": str(ns)},
				{"key": workloadAttr, "value": str(name)},
			}},
			"scopeSpans": []map[string]any{{
				"spans": []map[string]any{{
					"name": req.Method + " " + path, "kind": kindClient,
					"startTimeUnixNano": strconv.FormatInt(start.UnixNano(), 10),
					"endTimeUnixNano":   strconv.FormatInt(end.UnixNano(), 10),
					"attributes":        attrs,
				}},
			}},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	httpReq, err := http.NewRequest(http.MethodPost, cfg.OTLP, bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cfg.Token)
	res, err := cfg.HTTP.Do(httpReq)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
}

func clipBody(b []byte) []byte {
	if len(b) > bodyMax {
		return b[:bodyMax]
	}
	return b
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }
