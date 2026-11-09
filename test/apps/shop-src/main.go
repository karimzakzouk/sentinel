// Command shop is the source of a small test service the Fixer fixes the
// code of: hack/shop-up.sh puts this directory in the test Gitea as
// person/shop-src, and hack/ci.sh, standing in for its pipeline, builds an
// image of each commit, labelled with the commit it came from.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

func main() {
	catalog := load()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		it, ok := catalog[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(it)
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int{"items": len(catalog)})
	})
	log.Printf("shop listening on :8080 with %d items", len(catalog))
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
