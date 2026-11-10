package forge

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Fixer acts as a GitHub App: it signs a short token with the App's
// key, trades it for its installation's token, acts with that, and asks
// for a new one only as the last nears its hour.
func TestAGitHubAppActsWithItsInstallationsToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	parsed, err := ParseAppKey(pemKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)
	issued, used := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/7/access_tokens":
			jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			parts := strings.Split(jwt, ".")
			sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
			sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var c map[string]any
			_ = json.Unmarshal(claims, &c)
			if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) != nil || c["iss"] != "42" {
				http.Error(w, "bad JWT", http.StatusUnauthorized)
				return
			}
			issued++
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"token":"ghs_%d","expires_at":"%s"}`, issued, now.Add(time.Hour).Format(time.RFC3339))
		case r.URL.Path == "/repos/team/shop/pulls/3":
			used = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"number":3,"state":"open"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	app := &App{ID: 42, InstallationID: 7, Key: parsed, API: srv.URL, Now: func() time.Time { return now }}
	c := &Client{Kind: GitHub, Web: "https://github.com", API: srv.URL, App: app}
	for range 2 {
		if _, err := c.Get(context.Background(), "team", "shop", 3); err != nil {
			t.Fatal(err)
		}
	}
	if issued != 1 || used != "token ghs_1" {
		t.Fatalf("issued %d tokens, used %q", issued, used)
	}
	now = now.Add(56 * time.Minute)
	if _, err := c.Get(context.Background(), "team", "shop", 3); err != nil || issued != 2 || used != "token ghs_2" {
		t.Fatalf("near the hour: issued %d, used %q (%v)", issued, used, err)
	}
}
