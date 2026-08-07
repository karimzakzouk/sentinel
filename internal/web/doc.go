// Package web embeds and serves the Sentinel dashboard UI.
//
// The dashboard is a Next.js static export that lives under out/ at build
// time. The Go binary embeds it via //go:embed so a single artifact ships
// the API server + UI together — no separate static-file server required.
//
// At request time the handler:
//
//   - Serves pre-gzipped assets (.gz siblings) when the client advertises
//     gzip support, with the original Content-Type preserved so browsers
//     render instead of downloading.
//   - Falls back to the uncompressed file for clients that do not support
//     gzip (or when no .gz sibling exists).
//   - Falls back to index.html for any unknown path so client-side routes
//     (e.g. /incidents/abc123) work after a hard refresh.
//
// The frontend source itself is in /web at the repo root and is rebuilt by
// `make web`; this package only consumes its build output.
package web
