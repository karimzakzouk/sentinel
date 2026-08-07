package appprovision

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultManifest(t *testing.T) {
	m := DefaultManifest(
		"https://sentinel.example.com/webhook",
		"https://sentinel.example.com/callback",
		"https://sentinel.example.com/setup",
		"https://sentinel.example.com",
	)

	if m.Name != "Sentinel" {
		t.Errorf("Name: got %q, want Sentinel", m.Name)
	}
	if m.URL != "https://sentinel.example.com" {
		t.Errorf("URL: got %q", m.URL)
	}
	if m.HookAttributes == nil {
		t.Fatal("HookAttributes is nil")
	}
	if m.HookAttributes.URL != "https://sentinel.example.com/webhook" {
		t.Errorf("HookAttributes.URL: got %q", m.HookAttributes.URL)
	}
	if m.HookAttributes.ContentType != "json" {
		t.Errorf("ContentType: got %q, want json", m.HookAttributes.ContentType)
	}
	if !m.HookAttributes.Active {
		t.Error("HookAttributes.Active should be true")
	}
	if m.Public {
		t.Error("Public should be false")
	}
	if m.DefaultPermissions["contents"] != "write" {
		t.Errorf("contents permission: got %q, want write", m.DefaultPermissions["contents"])
	}
	if m.DefaultPermissions["pull_requests"] != "write" {
		t.Errorf("pull_requests permission: got %q, want write", m.DefaultPermissions["pull_requests"])
	}
	if len(m.DefaultEvents) == 0 {
		t.Error("DefaultEvents should not be empty")
	}
	if len(m.RedirectURLs) != 1 {
		t.Errorf("expected 1 redirect URL, got %d", len(m.RedirectURLs))
	}
}

// TestDefaultManifestEmptyWebhookURL verifies the appURL fallback when
// webhookURL is empty (GitHub would silently reject an empty URL).
func TestDefaultManifestEmptyWebhookURL(t *testing.T) {
	m := DefaultManifest("", "", "", "https://sentinel.example.com")
	if m.HookAttributes.URL != "https://sentinel.example.com" {
		t.Errorf("expected appURL fallback, got %q", m.HookAttributes.URL)
	}
}

func TestDefaultManifestNoRedirects(t *testing.T) {
	m := DefaultManifest("https://wh", "", "", "https://app")
	if len(m.RedirectURLs) != 0 {
		t.Errorf("expected 0 redirect URLs, got %d", len(m.RedirectURLs))
	}
	if m.SetupURL != "" {
		t.Errorf("expected empty setup URL, got %q", m.SetupURL)
	}
}

func TestEncodeManifest(t *testing.T) {
	m := DefaultManifest("https://wh", "https://cb", "https://setup", "https://app")
	encoded, err := EncodeManifest(m)
	if err != nil {
		t.Fatalf("EncodeManifest error: %v", err)
	}

	jsonBytes, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode error: %v", err)
	}

	var decoded Manifest
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("JSON unmarshal error: %v", err)
	}

	if decoded.Name != "Sentinel" {
		t.Errorf("decoded Name: got %q", decoded.Name)
	}
	if decoded.HookAttributes.ContentType != "json" {
		t.Errorf("decoded ContentType: got %q, want json", decoded.HookAttributes.ContentType)
	}
}

func TestCreateAppURL(t *testing.T) {
	m := DefaultManifest("https://wh", "", "", "https://app")

	url, err := CreateAppURL("", m)
	if err != nil {
		t.Fatalf("CreateAppURL error: %v", err)
	}
	if !strings.HasPrefix(url, "https://github.com/settings/apps/new?manifest=") {
		t.Errorf("URL should start with github.com, got %q", url[:50])
	}

	// Custom host (GitHub Enterprise).
	url2, err := CreateAppURL("github.enterprise.com", m)
	if err != nil {
		t.Fatalf("CreateAppURL error: %v", err)
	}
	if !strings.HasPrefix(url2, "https://github.enterprise.com/settings/apps/new?manifest=") {
		t.Errorf("URL should start with github.enterprise.com")
	}
}

func TestCodeExchangeResponseJSON(t *testing.T) {
	jsonStr := `{
		"id": 4162860,
		"slug": "sentinel-ai-bot",
		"node_id": "ABC123",
		"name": "Sentinel",
		"owner": {"login": "karimzakzouk"},
		"client_id": "Iv1.abc123",
		"client_secret": "secret123",
		"pem": "[REDACTED:ssh_private_key]\n...\n-----END RSA PRIVATE KEY-----",
		"webhook_secret": "whsecret123"
	}`

	var resp CodeExchangeResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if resp.ID != 4162860 {
		t.Errorf("ID: got %d", resp.ID)
	}
	if resp.Slug != "sentinel-ai-bot" {
		t.Errorf("Slug: got %q", resp.Slug)
	}
	if resp.Owner.Login != "karimzakzouk" {
		t.Errorf("Owner.Login: got %q", resp.Owner.Login)
	}
	if resp.PEM == "" {
		t.Error("PEM should not be empty")
	}
}
