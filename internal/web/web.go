// Package web embeds and serves the Sentinel dashboard UI.
package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

//go:embed all:out
var files embed.FS

func Handler() http.Handler {
	sub, err := fs.Sub(files, "out")
	if err != nil {
		return http.HandlerFunc(placeholderHandler)
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}

		// Serve pre-gzipped assets when the client supports gzip.
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			gzPath := strings.TrimPrefix(path, "/") + ".gz"
			if f, err := sub.Open(gzPath); err == nil {
				f.Close()
				// Set Content-Type based on the ORIGINAL file extension (not .gz)
				// Without this, http.FileServer sets Content-Type: application/gzip
				// and the browser downloads the file instead of rendering it.
				ct := mime.TypeByExtension(filepath.Ext(path))
				if ct == "" {
					ct = "application/octet-stream"
				}
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Type", ct)
				r.URL.Path = path + ".gz"
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// Fall back to uncompressed file.
		f, err := sub.Open(strings.TrimPrefix(path, "/"))
		if err != nil {
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		f.Close()
		fileServer.ServeHTTP(w, r)
	})
}

func placeholderHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html><html><head><title>Sentinel</title></head>
<body style="background:#0A0A0B;color:#A1A1AA;font-family:monospace;padding:40px;">
<h1 style="color:#06B6D4;">Sentinel</h1>
<p>Web UI not built. Run <code>make web && make build</code>.</p>
</body></html>`))
}
