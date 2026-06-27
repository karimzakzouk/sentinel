// Package githubapp provides GitHub App authentication.
//
// v3.8.0: Replaces personal access tokens with GitHub App installation tokens.
// This enables:
//   - One-click install from the GitHub Marketplace
//   - Verified "Sentinel" bot identity on PR review comments
//   - Automatic webhook registration on every repo in the org
//   - No token expiration management (installation tokens auto-refresh)
//
// Flow:
//   1. User creates a GitHub App (manual, one-time)
//   2. User installs it on their org/repos
//   3. GitHub sends "installation" webhook to Sentinel
//   4. Sentinel stores the installation ID
//   5. When a PR opens, Sentinel generates a JWT from the app's private key,
//      exchanges it for an installation token, and uses that to post review comments
package githubapp

import (
        "bytes"
        "context"
        "crypto/rsa"
        "encoding/json"
        "encoding/pem"
        "fmt"
        "io"
        "log"
        "net/http"
        neturl "net/url"
        "strings"
        "sync"
        "time"

        "github.com/golang-jwt/jwt/v5"
)

// App handles GitHub App authentication.
type App struct {
        appID         string
        privateKey    *rsa.PrivateKey
        installID     string
        httpClient    *http.Client
        apiURL        string // v3.11.0: base API URL (default "https://api.github.com")

        // Cached installation token
        tokenMu       sync.Mutex
        cachedToken   string
        tokenExpiry   time.Time
}

// New creates a GitHub App from the app ID, private key (PEM), and installation ID.
// v3.11.0: apiURL is the base API URL (default "https://api.github.com"). For
// GitHub Enterprise, pass "https://gh.mycorp.internal/api/v3". Empty defaults
// to the public GitHub API. CRITICAL audit #2.
func New(appID, privateKeyPEM, installID string) (*App, error) {
        return NewWithAPIURL(appID, privateKeyPEM, installID, "https://api.github.com")
}

// NewWithAPIURL creates a GitHub App with a custom API base URL. v3.11.0:
// For GitHub Enterprise / GitLab (GitHub-compat API) support.
func NewWithAPIURL(appID, privateKeyPEM, installID, apiURL string) (*App, error) {
        block, _ := pem.Decode([]byte(privateKeyPEM))
        if block == nil {
                return nil, fmt.Errorf("failed to parse PEM block containing private key")
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

// generateJWT creates a signed JWT for GitHub App authentication.
// GitHub requires RS256 algorithm, issued at time, and expiration (max 10 min).
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

// GetInstallToken returns a valid installation token, refreshing if needed.
// Installation tokens expire after 1 hour. We cache and refresh at 50 min.
func (a *App) GetInstallToken(ctx context.Context) (string, error) {
        a.tokenMu.Lock()
        defer a.tokenMu.Unlock()

        // Return cached token if still valid (with 10 min buffer)
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

        if resp.StatusCode != 201 {
                body, _ := io.ReadAll(resp.Body)
                return "", fmt.Errorf("GitHub returned %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
        }

        var result struct {
                Token     string    `json:"token"`
                ExpiresAt time.Time `json:"expires_at"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
                return "", fmt.Errorf("decode response: %w", err)
        }

        a.cachedToken = result.Token
        a.tokenExpiry = result.ExpiresAt

        return result.Token, nil
}

// PostReviewComment posts a PR review comment using the app's installation token.
// The comment appears as the verified "Sentinel" bot.
func (a *App) PostReviewComment(ctx context.Context, owner, repo string, prNumber int, body string) error {
        token, err := a.GetInstallToken(ctx)
        if err != nil {
                return fmt.Errorf("get install token: %w", err)
        }

        reviewBody := map[string]any{
                "body":  body,
                "event": "COMMENT",
        }
        payload, _ := json.Marshal(reviewBody)

        url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/reviews", a.apiURL, owner, repo, prNumber)
        req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
        if err != nil {
                return err
        }
        req.Header.Set("Accept", "application/vnd.github+json")
        req.Header.Set("Authorization", "Bearer "+token) // App installation token
        req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
        req.Header.Set("Content-Type", "application/json")

        resp, err := a.httpClient.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()

        if resp.StatusCode >= 400 {
                body, _ := io.ReadAll(resp.Body)
                return fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
        }
        return nil
}

// FetchPRDiff fetches the PR diff using the app's installation token.
// v3.18.0: SECURITY — diffURL is validated to be under api.github.com (or the
// configured GitHub Enterprise URL). Caller-supplied URLs from webhook bodies
// are an SSRF + token-exfiltration vector.
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

        if resp.StatusCode != 200 {
                return "", fmt.Errorf("diff fetch returned %d", resp.StatusCode)
        }

        body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB cap
        if err != nil {
                return "", err
        }
        return string(body), nil
}

// validateGitHubURL refuses any URL whose host is not the expected GitHub host.
// This blocks SSRF attacks where a webhook payload supplies an attacker-controlled URL.
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

// RegisterWebhook registers a webhook on a repo using the app's installation token.
// v3.8.0: Called when the app is installed, so every repo gets PR review automatically.
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

        if resp.StatusCode >= 400 && resp.StatusCode != 422 { // 422 = already exists
                respBody, _ := io.ReadAll(resp.Body)
                return fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
        }
        return nil
}

// HandleInstallationEvent processes a GitHub App installation webhook.
// v3.8.0: When the app is installed on an org, this auto-registers webhooks
// on every repo.
func (a *App) HandleInstallationEvent(ctx context.Context, payload []byte, webhookURL, secret string) error {
        var event struct {
                Action string `json:"action"`
                Installation struct {
                        ID int `json:"id"`
                        Account struct {
                                Login string `json:"login"`
                        } `json:"account"`
                } `json:"installation"`
                Repositories []struct {
                        Name  string `json:"name"`
                        FullName string `json:"full_name"`
                } `json:"repositories"`
        }

        if err := json.Unmarshal(payload, &event); err != nil {
                return fmt.Errorf("parse installation event: %w", err)
        }

        if event.Action != "created" && event.Action != "added" {
                return nil // Only handle new installations
        }

        // Register webhook on each repo
        for _, repo := range event.Repositories {
                owner := event.Installation.Account.Login
                repoName := repo.Name
                if err := a.RegisterWebhook(ctx, owner, repoName, webhookURL, secret); err != nil {
                        log.Printf("[github-app]  failed to register webhook on %s/%s: %v", owner, repoName, err)
                } else {
                        log.Printf("[github-app]  webhook registered on %s/%s", owner, repoName)
                }
        }

        return nil
}

