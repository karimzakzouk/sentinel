// Package api — small HTTP helpers.
package api

import (
        "encoding/json"
        "io"
        "net/http"
)


func writeJSON(w http.ResponseWriter, status int, v any) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(status)
        _ = json.NewEncoder(w).Encode(v)
}

// maxRequestBodyBytes limits the size of request bodies to prevent OOM DoS.

// limit. An attacker could POST a 10GB body and OOM the 256Mi pod.
// 1MB is generous for all legitimate API requests (settings JSON, investigate
// trigger JSON, pause JSON are all <2KB in practice).
const maxRequestBodyBytes = 1 << 20 // 1MB

func readBody(r *http.Request) (string, error) {

        r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
        data, err := io.ReadAll(r.Body)
        return string(data), err
}
