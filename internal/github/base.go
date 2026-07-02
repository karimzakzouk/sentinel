// Package githubapp — shared base client for GitHub API operations.
//
// BaseClient provides the common auth + HTTP + retry logic used by all
// GitHub clients in Sentinel:
//   - internal/chatops/github.go (comments, reactions, labels)
//   - internal/automerge/github.go (CI status, merge)
//   - internal/executors/github.go (branch, file, PR)
//
// Each client embeds BaseClient and adds its domain-specific methods.
package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// TokenProvider returns a fresh GitHub installation token for GitHub App auth.
// When set, all API calls use the installation token instead of the PAT.
type TokenProvider interface {
	GetInstallToken(ctx context.Context) (string, error)
}

// BaseClient handles auth + HTTP + retry for GitHub API calls.
// Embed it in domain-specific clients (chatops, automerge, executors).
type BaseClient struct {
	Token         string
	APIURL        string
	HTTP          *http.Client
	TokenProvider TokenProvider
}

// NewBaseClient creates a BaseClient with sensible defaults.
func NewBaseClient(token, apiURL string) *BaseClient {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	return &BaseClient{
		Token:  token,
		APIURL: apiURL,
		HTTP:   &http.Client{Timeout: 30 * time.Second},
	}
}

// WithTokenProvider sets a GitHub App token provider for installation-token auth.
func (b *BaseClient) WithTokenProvider(tp TokenProvider) {
	b.TokenProvider = tp
}

// AuthToken returns the current auth token — installation token if a
// TokenProvider is set, otherwise the PAT.
func (b *BaseClient) AuthToken(ctx context.Context) string {
	if b.TokenProvider != nil {
		if token, err := b.TokenProvider.GetInstallToken(ctx); err == nil && token != "" {
			return token
		}
	}
	return b.Token
}

// Do executes an authenticated GitHub API request with retry + rate-limit handling.
// `out` is populated from the response body (nil for no response parsing).
// Retries up to 3 times on 403/429 with exponential backoff.
func (b *BaseClient) Do(ctx context.Context, method, url string, body any, out any) error {
	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			backoff += time.Duration(time.Now().UnixNano()%500) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		var bodyReader io.Reader
		if body != nil {
			bBytes, err := json.Marshal(body)
			if err != nil {
				return err
			}
			bodyReader = bytes.NewReader(bBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+b.AuthToken(ctx))
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := b.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		// Rate limit / secondary rate limit
		if resp.StatusCode == 403 || resp.StatusCode == 429 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if isRateLimited(resp, body) {
				log.Printf("[github] rate limited (attempt %d/%d): %s", attempt+1, maxRetries+1, string(body[:min(len(body), 200)]))
				lastErr = fmt.Errorf("rate limited: %s", string(body[:min(len(body), 200)]))
				continue
			}
			// Non-rate-limit 403 = auth error, don't retry
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}

		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			respBody, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("github API %s %s: %d %s", method, url, resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
		}

		if out != nil && resp.StatusCode != 204 {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}

	return fmt.Errorf("github API %s %s: max retries exceeded: %w", method, url, lastErr)
}

// isRateLimited checks if a 403/429 response is due to rate limiting.
func isRateLimited(resp *http.Response, body []byte) bool {
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	if remaining == "0" {
		return true
	}
	bodyStr := string(body)
	return contains(bodyStr, "rate limit") || contains(bodyStr, "secondary rate limit")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
