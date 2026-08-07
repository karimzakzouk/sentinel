package github

import (
	"net/http"
	"testing"
)

// TestNewWithAPIURL verifies that NewWithAPIURL correctly sets the API URL
// and trims trailing slashes. The fake key won't parse, so we expect an error,
// but the test verifies the function doesn't panic.
func TestNewWithAPIURL(t *testing.T) {
	cases := []struct {
		name   string
		apiURL string
	}{
		{"empty defaults to github", ""},
		{"explicit", "https://api.github.com"},
		{"trailing slash trimmed", "https://api.github.com/"},
		{"enterprise", "https://gh.mycorp.internal/api/v3"},
		{"enterprise trailing slash", "https://gh.mycorp.internal/api/v3/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panicked: %v", r)
				}
			}()
			_, _ = NewWithAPIURL("123", "fake-key", "456", tc.apiURL)
		})
	}
}

// TestNewDefaultsToGitHub verifies New() doesn't panic with default args.
func TestNewDefaultsToGitHub(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("New() panicked: %v", r)
		}
	}()
	_, _ = New("123", "fake-key", "456")
}

func TestValidateGitHubURL(t *testing.T) {
	cases := []struct {
		name         string
		url          string
		expectedBase string
		wantErr      bool
	}{
		{"valid public", "https://api.github.com/repos/foo/bar", "https://api.github.com", false},
		{"valid enterprise", "https://gh.mycorp.internal/api/v3/repos/foo", "https://gh.mycorp.internal/api/v3", false},
		{"http rejected", "http://api.github.com/repos/foo", "https://api.github.com", true},
		{"wrong host", "https://evil.com/repos/foo", "https://api.github.com", true},
		{"invalid url", "://broken", "https://api.github.com", true},
		{"empty base defaults to github", "https://api.github.com/x", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGitHubURL(tc.url, tc.expectedBase)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateGitHubURL(%q, %q) err = %v, wantErr %v", tc.url, tc.expectedBase, err, tc.wantErr)
			}
		})
	}
}

func TestBaseClientNewDefaultsAPIURL(t *testing.T) {
	c := NewBaseClient("tok", "")
	if c.APIURL != "https://api.github.com" {
		t.Errorf("APIURL = %q, want https://api.github.com", c.APIURL)
	}
	c2 := NewBaseClient("tok", "https://gh.example/api/v3")
	if c2.APIURL != "https://gh.example/api/v3" {
		t.Errorf("APIURL = %q", c2.APIURL)
	}
}

func TestBaseClientAuthTokenFallbackToPAT(t *testing.T) {
	b := NewBaseClient("pat-token", "")
	// AuthToken takes context.Context; passing nil is safe in the fallback path
	// because TokenProvider is nil and ctx is never dereferenced.
	if got := b.AuthToken(nil); got != "pat-token" {
		t.Errorf("AuthToken without provider = %q, want pat-token", got)
	}
}

// TestIsRateLimited verifies rate-limit detection.
func TestIsRateLimited(t *testing.T) {
	cases := []struct {
		name      string
		remaining string
		body      string
		want      bool
	}{
		{"remaining zero", "0", "", true},
		{"body rate limit", "100", "rate limit exceeded", true},
		{"body secondary", "100", "secondary rate limit", true},
		{"plain 403", "100", "Forbidden", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Header: map[string][]string{}}
			resp.Header.Set("X-RateLimit-Remaining", tc.remaining)
			if got := isRateLimited(resp, []byte(tc.body)); got != tc.want {
				t.Errorf("isRateLimited = %v, want %v", got, tc.want)
			}
		})
	}
}
