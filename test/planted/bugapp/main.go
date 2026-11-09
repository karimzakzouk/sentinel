// Command bugapp is a small test service whose configuration comes from its
// environment. It checks every value as it starts and exits when one is
// wrong, the way real services do, so a bad value planted in Git is a crash
// on start that podinfo, which ignores unknown environment, cannot stand in
// for. It is built and loaded into the test cluster by hack/bugapp-up.sh.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	port, err := strconv.Atoi(get("LISTEN_PORT", "8080"))
	if err != nil || port < 1 || port > 65535 {
		log.Fatal("config: LISTEN_PORT is not a port")
	}
	ttl, err := time.ParseDuration(get("CACHE_TTL", "30s"))
	if err != nil {
		log.Fatal("config: CACHE_TTL is not a duration")
	}
	mode := get("MODE", "serve")
	if mode != "serve" && mode != "readonly" {
		log.Fatal("config: MODE is neither serve nor readonly")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("bugapp is running\n"))
	})
	log.Printf("bugapp listening on :%d (mode %s, cache ttl %s)", port, mode, ttl)
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func get(name, fallback string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return fallback
}
