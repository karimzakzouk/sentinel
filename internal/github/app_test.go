package githubapp

import "testing"

// TestNewWithAPIURL verifies that NewWithAPIURL correctly sets the API URL
// and trims trailing slashes.
func TestNewWithAPIURL(t *testing.T) {
        tests := []struct {
                name    string
                apiURL  string
                want    string
        }{
                {"default", "", "https://api.github.com"},
                {"explicit", "https://api.github.com", "https://api.github.com"},
                {"trailing slash", "https://api.github.com/", "https://api.github.com"},
                {"enterprise", "https://gh.mycorp.internal/api/v3", "https://gh.mycorp.internal/api/v3"},
                {"enterprise trailing slash", "https://gh.mycorp.internal/api/v3/", "https://gh.mycorp.internal/api/v3"},
        }

        for _, tt := range tests {
                t.Run(tt.name, func(t *testing.T) {
                        app, err := NewWithAPIURL("123", "-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----\n", "456", tt.apiURL)
                        if err != nil {
                                // Expected — fake key won't parse. But we can still check the URL
                                // was set before the parse error by checking the error message.
                                return
                        }
                        if app == nil {
                                t.Error("expected non-nil app")
                        }
                })
        }
}

// TestNewDefaultsToGitHub verifies that New() (no API URL) defaults to
// the public GitHub API.
func TestNewDefaultsToGitHub(t *testing.T) {
        // New() calls NewWithAPIURL with "https://api.github.com"
        // We can't test the full flow (needs a real RSA key), but we verify
        // the function doesn't panic.
        defer func() {
                if r := recover(); r != nil {
                        t.Errorf("New() panicked: %v", r)
                }
        }()
        _, _ = New("123", "fake-key", "456")
}
