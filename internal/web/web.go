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

// Handler returns an http.Handler that serves the embedded dashboard.
//
// The handler prefers pre-gzipped assets (sibling .gz files) when the
// client supports gzip, otherwise serves the uncompressed file. Unknown
// paths fall back to index.html so client-side routing survives a hard
// refresh. If the embedded out/ tree is missing (binary built without
// the frontend), a small placeholder HTML page is served instead.
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

		// Serve the pre-gzipped sibling when it exists. The Content-Type
		// is set from the original extension — without this http.FileServer
		// would set application/gzip and the browser would download the
		// asset instead of executing it.
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			gzPath := strings.TrimPrefix(path, "/") + ".gz"
			if f, err := sub.Open(gzPath); err == nil {
				f.Close()
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

		// Fall back to the uncompressed file. If that is missing too,
		// rewrite to / so the SPA shell takes over client-side routing.
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

// placeholderHandler is served when the embedded out/ tree is missing —
// i.e. the binary was built without running `make web` first.
func placeholderHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html><html><head><title>Sentinel</title></head>
<body style="background:#0A0A0B;color:#A1A1AA;font-family:monospace;padding:40px;">
<h1 style="color:#06B6D4;">Sentinel</h1>
<p>Web UI not built. Run <code>make web &amp;&amp; make build</code>.</p>
</body></html>`))
}
