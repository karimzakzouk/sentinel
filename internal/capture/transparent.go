package capture

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// ServeTransparentMITM listens for REDIRECT'd TLS connections, terminates them
// with the capture CA using the original destination (and SNI), dials the real
// origin, and posts bodies like ServeMITM. Call ApplyRedirect before this.
func ServeTransparentMITM(cfg MITMConfig) error {
	if cfg.Listen == "" {
		return fmt.Errorf("capture-mitm needs --listen")
	}
	if _, err := strconv.Atoi(cfg.Listen); err == nil {
		cfg.Listen = ":" + cfg.Listen
	}
	certPEM, err := os.ReadFile(cfg.CACertFile) //nolint:gosec // operator-mounted capture CA
	if err != nil {
		return fmt.Errorf("read capture CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(cfg.CAKeyFile) //nolint:gosec // operator-mounted capture CA
	if err != nil {
		return fmt.Errorf("read capture CA key: %w", err)
	}
	ca, err := AuthorityFromPEM(certPEM, keyPEM)
	if err != nil {
		return err
	}
	if cfg.DialTLS == nil {
		cfg.DialTLS = func(network, addr string) (net.Conn, error) {
			return tls.Dial(network, addr, &tls.Config{MinVersion: tls.VersionTLS12}) //nolint:gosec // dials real origins with system trust
		}
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	certs := &certCache{ca: ca}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go handleTransparent(conn, certs, cfg)
	}
}

func handleTransparent(conn net.Conn, certs *certCache, cfg MITMConfig) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	hostPort, err := originalDestination(conn)
	if err != nil {
		return
	}
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return
	}
	if port == "" {
		port = "443"
		hostPort = net.JoinHostPort(host, port)
	}
	clientIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	var sni string
	tlsClient := tls.Server(conn, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := host
			if chi.ServerName != "" {
				sni = strings.ToLower(chi.ServerName)
				name = sni
			}
			return certs.certificate(name)
		},
	})
	if err := tlsClient.Handshake(); err != nil {
		return
	}
	defer tlsClient.Close()
	dialHost := hostPort
	if sni != "" && net.ParseIP(sni) == nil {
		// Prefer SNI hostname for the upstream name when REDIRECT only saw an IP.
		_, p, _ := net.SplitHostPort(hostPort)
		dialHost = net.JoinHostPort(sni, p)
		host = sni
	}
	upstream, err := dialOrigin(cfg, clientIP, dialHost)
	if err != nil {
		return
	}
	defer upstream.Close()
	proxyCapture(tlsClient, upstream, host, clientIP, cfg)
}
