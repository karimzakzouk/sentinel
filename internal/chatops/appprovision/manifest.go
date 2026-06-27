// Package appprovision implements the GitHub App auto-provisioning flow.
//
// v4.1.0: Implements the "manifest flow" — a one-command setup that creates
// a GitHub App in the user's account with all permissions pre-configured.
// The user clicks one button on GitHub, GitHub redirects back with a code,
// Sentinel exchanges the code for the App's PEM + ID + webhook secret, and
// stores everything in runtime config. No manual App creation, no PEM file
// management, no permission dropdowns.
//
// This is the same flow Probot, Renovate, and Glitch use. Works on both
// github.com and GitHub Enterprise Server.
//
// Flow:
//   1. sentinelctl app setup --server http://sentinel:8000
//   2. CLI spins up a localhost HTTP server on a random port
//   3. CLI opens browser to https://github.com/settings/apps/new?manifest=<base64>
//   4. User sees a pre-filled "Create GitHub App" form, clicks Create
//   5. GitHub redirects to http://localhost:<port>/callback?code=<code>
//   6. CLI POSTs the code to GitHub's /app-manifests/{code}/conversions endpoint
//   7. GitHub returns { id, slug, pem, webhook_secret, client_id, client_secret }
//   8. CLI POSTs the result to Sentinel's /settings/github-app endpoint
//   9. Sentinel stores the App credentials in runtime config + wires them live
//
// Total user effort: one click. Total time: ~30 seconds.
package appprovision

import (
	"encoding/base64"
	"encoding/json"
)

// Manifest is the GitHub App manifest submitted via the
// https://github.com/settings/apps/new?manifest=<base64> URL.
// GitHub pre-fills the "Create GitHub App" form with these values.
//
// Docs: https://docs.github.com/en/apps/sharing-github-apps/sharing-your-github-app-with-other-users
type Manifest struct {
	Name            string                 `json:"name"`              // "Sentinel" (user can edit)
	URL             string                 `json:"url"`               // App homepage URL
	HookAttributes  HookAttributes         `json:"hook_attributes"`   // Webhook config
	RedirectURLs    []string               `json:"redirect_urls"`     // Where GitHub sends the code after creation
	CallbackURLs    []string               `json:"callback_urls"`     // Legacy — same as redirect_urls
	SetupURL        string                 `json:"setup_url"`         // Where GitHub sends the user after install
	Public          bool                   `json:"public"`            // Whether the App is installable by others
	Description     string                 `json:"description"`       // App description
	DefaultPermissions mapstringPermission `json:"default_permissions"` // Repo permissions
	DefaultEvents   []string               `json:"default_events"`    // Webhook event subscriptions
}

// HookAttributes configures the webhook endpoint.
type HookAttributes struct {
	URL    string `json:"url"`    // Sentinel's /webhook/github endpoint
	Active bool   `json:"active"` // Whether webhooks fire
	// ContentType defaults to "application/json" if not set; we let GitHub pick
}

// mapstringPermission is a type alias for the permissions map.
// Keys are permission names ("contents", "pull_requests", etc.),
// values are "read" or "write".
type mapstringPermission = map[string]string

// DefaultManifest returns the manifest for Sentinel. The user can override:
//   - webhookURL: where GitHub sends webhook events (Sentinel's /webhook/github)
//   - redirectURL: where GitHub sends the post-create code (CLI's localhost)
//   - setupURL: where GitHub sends the user after install (Sentinel's UI)
//   - appURL: the App's homepage URL (Sentinel's repo or install URL)
//
// Permissions chosen based on what Sentinel actually does:
//   - contents (RW): clone repo + push branches
//   - pull_requests (RW): open PRs + post comments + react + merge
//   - issues (RW): post comments + react + label management (PRs are issues in the API)
//   - metadata (R): always required by GitHub
//   - statuses (R): read CI status for auto-merge
//   - members (R): permission checks for chatops (/sentinel <cmd>)
//
// Events chosen based on what Sentinel listens to:
//   - issue_comment: chatops commands
//   - pull_request: predictive PR review (existing)
//   - check_run: CI status for auto-merge
//   - status: legacy CI status
//   - installation: auto-capture install ID when user installs the App
func DefaultManifest(webhookURL, redirectURL, setupURL, appURL string) Manifest {
	return Manifest{
		Name:        "Sentinel",
		URL:         appURL,
		Description: "Autonomous AI SRE — detects incidents, writes fixes, opens PRs. https://github.com/karimzakzouk/sentinel",
		Public:      false, // Per-user App — not installable by others (the user installs their own)
		HookAttributes: HookAttributes{
			URL:    webhookURL,
			Active: webhookURL != "", // If no webhook URL, webhooks are disabled (PAT-style)
		},
		RedirectURLs: []string{redirectURL},
		CallbackURLs: []string{redirectURL},
		SetupURL:     setupURL,
		DefaultPermissions: mapstringPermission{
			"contents":       "write",
			"pull_requests":  "write",
			"issues":         "write",
			"metadata":       "read",
			"statuses":       "read",
			"members":        "read",
			"organization_administration": "read", // for permission checks in chatops
		},
		DefaultEvents: []string{
			"issue_comment",
			"pull_request",
			"check_run",
			"status",
			"installation",
		},
	}
}

// EncodeManifest returns the base64-encoded JSON manifest for the GitHub URL.
// The URL format is: https://github.com/settings/apps/new?manifest=<base64>
func EncodeManifest(m Manifest) (string, error) {
	jsonBytes, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(jsonBytes), nil
}

// CreateAppURL returns the GitHub URL the user visits to create the App.
// host defaults to "github.com"; for GitHub Enterprise, pass "github.example.com".
func CreateAppURL(host string, manifest Manifest) (string, error) {
	if host == "" {
		host = "github.com"
	}
	encoded, err := EncodeManifest(manifest)
	if err != nil {
		return "", err
	}
	return "https://" + host + "/settings/apps/new?manifest=" + encoded, nil
}

// CodeExchangeResponse is what GitHub returns after POSTing the code to
// /app-manifests/{code}/conversions.
//
// Docs: https://docs.github.com/en/rest/apps/apps#create-a-github-app-from-a-manifest
type CodeExchangeResponse struct {
	ID            int64  `json:"id"`             // App ID (numeric)
	Slug          string `json:"slug"`           // App slug (used in the bot username)
	NodeID        string `json:"node_id"`
	Name          string `json:"name"`           // "Sentinel"
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	PEM          string `json:"pem"`             // The private key (RSA, PEM-encoded)
	WebhookSecret string `json:"webhook_secret"`
}
