// Package github provides GitHub API clients for Sentinel.
//
// It exposes two layers:
//
//   - App: GitHub App authentication (RSA-signed JWT → installation token).
//     Used when an operator has installed the Sentinel GitHub App.
//   - BaseClient: shared HTTP + auth + retry for GitHub REST calls. Other
//     packages embed it to build domain-specific clients (chatops, automerge,
//     executors).
//
// Auth model: when a TokenProvider (typically *App) is set on a BaseClient,
// each request uses a fresh installation token. Otherwise the PAT (Token
// field) is used as the fallback.
package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/karimzakzouk/sentinel/internal/logging"
)

// App authenticates as a GitHub App installation.
type App struct {
	appID      string
	privateKey *rsa.PrivateKey
	installID  string
	httpClient *http.Client
	apiURL     string

	tokenMu     sync.Mutex
	cachedToken string
	tokenExpiry time.Time
}

// New creates an App from the app ID, PEM-encoded private key, and
// installation ID. Uses the public GitHub API.
func New(appID, privateKeyPEM, installID string) (*App, error) {
	return NewWithAPIURL(appID, privateKeyPEM, installID, "https://api.github.com")
}

// NewWithAPIURL creates an App with a custom API base URL. For GitHub
// Enterprise, pass "https://gh.mycorp.internal/api/v3". A trailing slash
// is trimmed.
func NewWithAPIURL(appID, privateKeyPEM, installID, apiURL string) (*App, error) {
	// Validate the PEM block up front. pem.Decode returns nil + the rest of
	// the input on failure; we surface that as a parse error to give the
	// caller an early, actionable message instead of relying on the JWT
	// parser's less obvious failure.
	if privateKeyPEM != "" {
		if _, rest := pem.Decode([]byte(privateKeyPEM)); len(rest) == len(privateKeyPEM) {
			return nil, fmt.Errorf("parse RSA private key: not a valid PEM block")
		}
	}

	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse RSA private key: %w", err)
	}

	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	apiURL = strings.TrimRight(apiURL, "/")

	return &App{
		appID:      appID,
		privateKey: key,
		installID:  installID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		apiURL:     apiURL,
	}, nil
}

// APIURL returns the configured API base URL.
func (a *App) APIURL() string { return a.apiURL }

// generateJWT creates a signed JWT for GitHub App authentication. GitHub
// requires RS256, with an issued-at and expiration (max 10 min).
func (a *App) generateJWT() (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": a.appID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(a.privateKey)
}

// GetInstallToken returns a valid installation token, refreshing the cached
// token if it is within 10 minutes of expiry.
func (a *App) GetInstallToken(ctx context.Context) (string, error) {
	a.tokenMu.Lock()
	defer a.tokenMu.Unlock()

	if a.cachedToken != "" && time.Now().Before(a.tokenExpiry.Add(-10*time.Minute)) {
		return a.cachedToken, nil
	}

	jwtToken, err := a.generateJWT()
	if err != nil {
		return "", fmt.Errorf("generate JWT: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", a.apiURL, a.installID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request install token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("github access_tokens: %d %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode access_tokens response: %w", err)
	}

	a.cachedToken = result.Token
	a.tokenExpiry = result.ExpiresAt
	return result.Token, nil
}

// PostReviewComment posts a PR review comment using the App's installation
// token. The comment appears as the verified App bot identity.
func (a *App) PostReviewComment(ctx context.Context, owner, repo string, prNumber int, body string) error {
	token, err := a.GetInstallToken(ctx)
	if err != nil {
		return fmt.Errorf("get install token: %w", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"body":  body,
		"event": "COMMENT",
	})
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", a.apiURL, owner, repo, prNumber)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("github post review: %d %s", resp.StatusCode, string(body))
	}
	return nil
}

// FetchPRDiff fetches the PR diff at diffURL using the App's installation
// token. diffURL is validated against a.apiURL to prevent SSRF + token
// exfiltration via webhook-supplied URLs.
func (a *App) FetchPRDiff(ctx context.Context, diffURL string) (string, error) {
	if err := validateGitHubURL(diffURL, a.apiURL); err != nil {
		return "", fmt.Errorf("refusing to fetch diff: %w", err)
	}
	token, err := a.GetInstallToken(ctx)
	if err != nil {
		return "", fmt.Errorf("get install token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", diffURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3.diff")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("diff fetch: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// validateGitHubURL refuses any URL whose host is not the expected GitHub
// host. Blocks SSRF where a webhook payload supplies an attacker-controlled URL.
func validateGitHubURL(rawURL, expectedBase string) error {
	if expectedBase == "" {
		expectedBase = "https://api.github.com"
	}
	expectedHost := strings.TrimPrefix(strings.TrimPrefix(expectedBase, "https://"), "http://")
	if i := strings.Index(expectedHost, "/"); i >= 0 {
		expectedHost = expectedHost[:i]
	}
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme must be https, got %q", u.Scheme)
	}
	if u.Host != expectedHost {
		return fmt.Errorf("host %q does not match expected GitHub host %q", u.Host, expectedHost)
	}
	return nil
}

// RegisterWebhook registers a webhook on owner/repo using the App's
// installation token. Called when the App is installed on an org, so every
// repo gets PR review automatically.
func (a *App) RegisterWebhook(ctx context.Context, owner, repo, webhookURL, secret string) error {
	token, err := a.GetInstallToken(ctx)
	if err != nil {
		return fmt.Errorf("get install token: %w", err)
	}
	payload := map[string]any{
		"config": map[string]any{
			"url":          webhookURL,
			"content_type": "json",
			"secret":       secret,
			"insecure_ssl": "0",
		},
		"events": []string{"pull_request"},
		"active": true,
	}
	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/repos/%s/%s/hooks", a.apiURL, owner, repo)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("register webhook: %d %s", resp.StatusCode, string(body))
	}
	return nil
}

// HandleInstallationEvent processes a GitHub App installation webhook. When
// the App is installed on an org or new repos are added, this auto-registers
// webhooks on every repo in the payload.
func (a *App) HandleInstallationEvent(ctx context.Context, payload []byte, webhookURL, secret string) error {
	var event struct {
		Action       string `json:"action"`
		Installation struct {
			ID      int `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
		} `json:"installation"`
		Repositories []struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("parse installation event: %w", err)
	}
	if event.Action != "created" && event.Action != "added" {
		return nil
	}
	owner := event.Installation.Account.Login
	for _, repo := range event.Repositories {
		if err := a.RegisterWebhook(ctx, owner, repo.Name, webhookURL, secret); err != nil {
			logging.Error("failed to register webhook", "owner", owner, "repo", repo.Name, "err", err)
		} else {
			logging.Info("webhook registered", "owner", owner, "repo", repo.Name)
		}
	}
	return nil
}
