// Package appprovision implements the GitHub App auto-provisioning flow.
// Implements the "manifest flow" — a one-command setup that creates
// a GitHub App in the user's account with all permissions pre-configured.
// The user clicks one button on GitHub, GitHub redirects back with a code,
// Sentinel exchanges the code for the App's PEM + ID + webhook secret, and
// stores everything in runtime config. No manual App creation, no PEM file
// management, no permission dropdowns.
//   - hook_attributes.url was empty when no --webhook-url was passed
//   - GitHub silently rejects manifests with empty hook_attributes.url
//   - content_type was missing (GitHub defaults to "form", we need "json")
// Now: if webhookURL is empty, we use the appURL as a placeholder (the user
// can change it later in App settings). We also always set content_type=json.
package appprovision

import (
	"encoding/base64"
	"encoding/json"
)

// Manifest is the GitHub App manifest submitted via the
// https://github.com/settings/apps/new?manifest=<base64> URL.
// GitHub pre-fills the "Create GitHub App" form with these values.
// Docs: https://docs.github.com/en/apps/sharing-github-apps/sharing-your-github-app-with-other-users

type Manifest struct {
	Name               string             `json:"name"`
	URL                string             `json:"url"`
	HookAttributes     *HookAttributes    `json:"hook_attributes,omitempty"`
	RedirectURLs       []string           `json:"redirect_urls,omitempty"`
	CallbackURLs       []string           `json:"callback_urls,omitempty"`
	SetupURL           string             `json:"setup_url,omitempty"`
	Public             bool               `json:"public"`
	Description        string             `json:"description,omitempty"`
	DefaultPermissions map[string]string  `json:"default_permissions"`
	DefaultEvents      []string           `json:"default_events,omitempty"`
}

// HookAttributes configures the webhook endpoint.

type HookAttributes struct {
	URL         string `json:"url"`
	Active      bool   `json:"active"`
	ContentType string `json:"content_type,omitempty"`
}

// DefaultManifest returns the manifest for Sentinel.
// If webhookURL is empty, we use appURL as a placeholder for
// hook_attributes.url. GitHub REQUIRES a valid http(s) URL — an empty
// string makes GitHub silently reject the manifest and show a blank form.
// The user can change the webhook URL later in App settings.

func DefaultManifest(webhookURL, redirectURL, setupURL, appURL string) Manifest {
	if webhookURL == "" {
		webhookURL = appURL
	}

	m := Manifest{
		Name:        "Sentinel",
		URL:         appURL,
		Description: "Autonomous AI SRE — detects incidents, writes fixes, opens PRs. https://github.com/karimzakzouk/sentinel",
		Public:      false,
		HookAttributes: &HookAttributes{
			URL:         webhookURL,
			Active:      true,
			ContentType: "json",
		},
		DefaultPermissions: map[string]string{
			"contents":                     "write",
			"pull_requests":                "write",
			"issues":                       "write",
			"metadata":                     "read",
			"statuses":                     "read",
			"members":                      "read",
			"organization_administration":  "read",
		},
		DefaultEvents: []string{
			"issue_comment",
			"pull_request",
			"check_run",
			"status",
			"installation",
		},
	}
	if redirectURL != "" {
		m.RedirectURLs = []string{redirectURL}
		m.CallbackURLs = []string{redirectURL}
	}
	if setupURL != "" {
		m.SetupURL = setupURL
	}
	return m
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

type CodeExchangeResponse struct {
	ID             int64  `json:"id"`
	Slug           string `json:"slug"`
	NodeID         string `json:"node_id"`
	Name           string `json:"name"`
	Owner          struct {
		Login string `json:"login"`
	} `json:"owner"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
}
