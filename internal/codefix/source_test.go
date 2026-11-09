package codefix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseReference(t *testing.T) {
	for in, want := range map[string]Reference{
		"redis":                                  {"registry-1.docker.io", "library/redis", "latest", ""},
		"bitnami/redis:7":                        {"registry-1.docker.io", "bitnami/redis", "7", ""},
		"ghcr.io/x/shop:3f2a9c1":                 {"ghcr.io", "x/shop", "3f2a9c1", ""},
		"registry.test:5000/shop/cart@sha256:ab": {"registry.test:5000", "shop/cart", "", "sha256:ab"},
		"localhost/app:dev":                      {"localhost", "app", "dev", ""},
	} {
		got, err := ParseReference(in)
		if err != nil || got != want {
			t.Errorf("%s: %+v, %v; want %+v", in, got, err, want)
		}
	}
}

// Two identities must agree on the commit: the labels, and the tag or the
// version.
func TestResolveSourceNeedsTwoThatAgree(t *testing.T) {
	labels := map[string]string{"org.opencontainers.image.source": "http://gitea/person/shop",
		"org.opencontainers.image.revision": "3f2a9c1e0b7d4c55aa0e9c1b2d3e4f5a6b7c8d9e"}
	if b, err := ResolveSource("registry.test/shop:3f2a9c1", labels); err != nil || b.Revision != labels["org.opencontainers.image.revision"] {
		t.Fatalf("a tag of the commit: %+v, %v", b, err)
	}
	if _, err := ResolveSource("registry.test/shop:sha-3f2a9c1e", labels); err != nil {
		t.Fatalf("a sha- tag: %v", err)
	}
	if _, err := ResolveSource("registry.test/shop:v1.4.0", labels); err == nil || !strings.Contains(err.Error(), "only") {
		t.Fatalf("labels alone: %v", err)
	}
	if _, err := ResolveSource("registry.test/shop:abcdef1", labels); err == nil {
		t.Fatal("a tag of another commit agreed")
	}
	if _, err := ResolveSource("registry.test/shop:3f2a9c1", map[string]string{}); err == nil {
		t.Fatal("an image without labels resolved")
	}
}

// Labels are read as a runtime pulls: a token for a 401, an index to the
// platform's image, and its config.
func TestRegistryReadsLabels(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("scope") != "repository:x/shop:pull" {
				http.Error(w, "scope", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "t0ken"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer t0ken" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="registry.test"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v2/x/shop/manifests/3f2a9c1":
			_, _ = w.Write([]byte(`{"mediaType": "application/vnd.oci.image.index.v1+json", "manifests": [
				{"digest": "sha256:arm", "platform": {"os": "linux", "architecture": "arm64"}},
				{"digest": "sha256:amd", "platform": {"os": "linux", "architecture": "amd64"}}]}`))
		case "/v2/x/shop/manifests/sha256:amd":
			_, _ = w.Write([]byte(`{"config": {"digest": "sha256:cfg"}}`))
		case "/v2/x/shop/blobs/sha256:cfg":
			_, _ = w.Write([]byte(`{"config": {"Labels": {"org.opencontainers.image.revision": "3f2a9c1e"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")
	g := &Registry{HTTP: srv.Client()}
	labels, err := g.Labels(context.Background(), host+"/x/shop:3f2a9c1")
	if err != nil || labels["org.opencontainers.image.revision"] != "3f2a9c1e" {
		t.Fatalf("labels %v, %v", labels, err)
	}
}
