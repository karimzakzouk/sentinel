package lab

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// ServeFakeTLS answers recorded HTTP statuses over TLS on listen (a port or
// host:port), using the Lab-issued certificate and the routes file the stub
// mounts. It blocks until the listener fails.
func ServeFakeTLS(listen, certFile, keyFile, routesFile string) error {
	if listen == "" {
		return fmt.Errorf("lab-fake needs --listen")
	}
	if _, err := strconv.Atoi(listen); err == nil {
		listen = ":" + listen
	}
	raw, err := os.ReadFile(routesFile) //nolint:gosec // path is the Lab stub's mounted ConfigMap
	if err != nil {
		return fmt.Errorf("read routes: %w", err)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("load Lab-issued certificate: %w", err)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv := &http.Server{
		Handler:           fakeHTTPHandler(parseRoutes(string(raw))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.Serve(tls.NewListener(ln, cfg))
}
