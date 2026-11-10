package forge

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// App is a GitHub App installation: the Fixer acts as the App, with tokens
// GitHub issues for one installation, scoped to the repositories it was
// installed on and good for an hour, instead of a person's long-lived
// token.
type App struct {
	ID             int64
	InstallationID int64
	Key            *rsa.PrivateKey
	// API is GitHub's API, https://api.github.com by default.
	API  string
	HTTP *http.Client
	Now  func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// ParseAppKey reads a GitHub App's private key, as GitHub hands it out:
// PEM, PKCS#1 or PKCS#8.
func ParseAppKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("the App's private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("read the App's private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the App's private key is not an RSA key")
	}
	return rk, nil
}

// Token is the installation's current token, asked for again five minutes
// before the last one expires.
func (a *App) Token(ctx context.Context) (string, error) {
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && now().Before(a.expires.Add(-5*time.Minute)) {
		return a.token, nil
	}
	jwt, err := a.jwt(now())
	if err != nil {
		return "", err
	}
	api := a.API
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/app/installations/%d/access_tokens", strings.TrimSuffix(api, "/"), a.InstallationID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("ask GitHub for the App's token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", &StatusError{Code: resp.StatusCode, What: "the App's installation token: " + resp.Status + ": " + string(bytes.TrimSpace(data))}
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("read the App's installation token: %w", err)
	}
	if out.Token == "" {
		return "", errors.New("GitHub answered with no installation token")
	}
	a.token, a.expires = out.Token, out.ExpiresAt
	return a.token, nil
}

// jwt is the App's own short token, signed with its key, that asks for an
// installation token: issued a minute back against clock skew, good for
// nine minutes, under GitHub's ten.
func (a *App) jwt(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": fmt.Sprint(a.ID)})
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
