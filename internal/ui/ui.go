// Package ui embeds Sentinel's web app, built from ui/ into dist/.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const notBuilt = `<!doctype html><meta charset="utf-8"><title>Sentinel</title>
<p>Sentinel's UI was not built into this binary. Run <code>make ui</code>, then build again.
The API answers under <a href="/api/summary">/api/</a>.</p>`

// Handler serves the web app. Paths that are not files get index.html, so
// the app's own routes survive a reload.
func Handler() http.Handler {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	if _, err := fs.Stat(files, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(notBuilt))
		})
	}
	static := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if _, err := fs.Stat(files, name); name == "" || err != nil {
			http.ServeFileFS(w, r, files, "index.html")
			return
		}
		if strings.HasPrefix(name, "assets/") {
			// Built asset names carry a content hash.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		static.ServeHTTP(w, r)
	})
}
