package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// maxRequestBodyBytes limits the size of request bodies to prevent OOM DoS.
// 1MB is generous for all legitimate API requests (settings JSON,
// investigate trigger JSON, pause JSON are all <2KB in practice).
const maxRequestBodyBytes = 1 << 20

// writeJSON writes v as JSON with the given HTTP status. A failed encode
// is logged but not surfaced to the client — the status header is already
// written by then.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logging.Info("api: writeJSON encode error", "err", err)
	}
}

// readBody reads up to maxRequestBodyBytes from r.Body and returns it as
// a string. Returns an error if the body exceeds the limit.
func readBody(r *http.Request) (string, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
