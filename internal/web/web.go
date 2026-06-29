// Package web embeds the Sentinel dashboard UI into the Go binary.
// The UI is a Next.js static export built by `make web`. It's served at `/`
// by the API server. If the UI hasn't been built yet, a placeholder page is
// shown instructing the user to run `make web`.
// Build flow:
//   1. `make web` runs `npm install && npm run build:static` in `web/`
//   2. The built files are copied to `internal/web/out/`
//   3. This file embeds them via `go:embed`
//   4. The Go binary serves them at `/`
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:out

var files embed.FS

// Handler returns an http.Handler that serves the embedded web UI.
// SPA-style routing: any path that doesn't match a static file falls back
// to index.html (so client-side routing works).

func Handler() http.Handler {
	sub, err := fs.Sub(files, "out")
	if err != nil {
		return http.HandlerFunc(placeholderHandler)
	}

	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if the file exists

		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}

		// Try to open the file

		f, err := sub.Open(strings.TrimPrefix(path, "/"))
		if err != nil {
			// File not found — serve index.html for SPA routing

			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		f.Close()

		// File exists — serve it

		fileServer.ServeHTTP(w, r)
	})
}

// placeholderHandler is shown when the web UI hasn't been built yet.

func placeholderHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Sentinel — Web UI Not Built</title></head>
<body style="background:#0A0A0B;color:#A1A1AA;font-family:monospace;padding:40px;">
<h1 style="color:#06B6D4;">Sentinel</h1>
<p>Web UI not built. Run <code style="color:#22C55E;background:#18181B;padding:2px 6px;border-radius:3px;">make web</code> to build it.</p>
<p>Then rebuild the binary: <code style="color:#22C55E;background:#18181B;padding:2px 6px;border-radius:3px;">make build</code></p>
</body>
</html>`))
}
