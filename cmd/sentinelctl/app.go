// Package main — `sentinelctl app` subcommand: GitHub App manifest flow.
//
// Flow:
//  1. sentinelctl spins up a localhost HTTP server on a random port.
//  2. Builds a GitHub App manifest with all permissions pre-configured.
//  3. Opens the user's browser to https://github.com/settings/apps/new?manifest=<base64>.
//  4. User clicks "Create GitHub App" on GitHub (form is pre-filled).
//  5. GitHub redirects to http://localhost:<port>/callback?code=<code>.
//  6. CLI POSTs the code to GitHub's /app-manifests/{code}/conversions endpoint.
//  7. GitHub returns { id, pem, webhook_secret, ... }.
//  8. CLI POSTs the App ID + PEM to Sentinel's /settings/github-app endpoint,
//     which wires the App into all live GitHub clients (no restart).
//  9. CLI opens the install URL so the user can install the App on a repo.
//  10. After install, the user runs `sentinelctl app set-install <install-id>`
//     with the install ID copied from the URL.
//
// The install ID is a separate step because GitHub's manifest flow does not
// auto-install the App — the user has to visit the install URL, pick a repo,
// and the install ID is in the URL after install. Auto-detecting this would
// require the `installation` webhook event (only fires when Sentinel has a
// public webhook URL); for local-dev / air-gapped setups the manual paste is
// the only way.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/karimzakzouk/sentinel/internal/chatops/appprovision"
)

// handleApp dispatches `sentinelctl app <setup|set-install|status|remove>`.
func (c *Client) handleApp(positional []string) {
	if len(positional) == 0 {
		printAppHelp()
		return
	}
	switch positional[0] {
	case "setup":
		c.handleAppSetup(positional[1:])
	case "set-install":
		c.handleAppSetInstall(positional[1:])
	case "status":
		c.handleAppStatus()
	case "remove":
		c.handleAppRemove()
	case "help", "--help", "-h":
		printAppHelp()
	default:
		fmt.Fprintf(os.Stderr, "error: unknown app subcommand: %s\n\n", positional[0])
		printAppHelp()
		os.Exit(1)
	}
}

// printAppHelp writes the `app` subcommand usage to stdout.
func printAppHelp() {
	fmt.Print(`sentinelctl app — manage the GitHub App for bot identity

Usage:
  sentinelctl app setup [--github-host <host>] [--webhook-url <url>]
  sentinelctl app set-install <install-id>
  sentinelctl app status
  sentinelctl app remove

Subcommands:
  setup        Create a GitHub App in your account with all permissions
               pre-configured (one click on GitHub).
  set-install  Set the installation ID after installing the App on your repo.
               The install ID is in the URL after install:
                 https://github.com/settings/installations/<ID>
  status       Show whether the GitHub App is configured.
  remove       Remove the GitHub App config (fall back to PAT).

Examples:
  sentinelctl app setup
  sentinelctl app setup --github-host github.example.com
  sentinelctl app set-install 12345678
  sentinelctl app status

After 'app setup' + 'app set-install', Sentinel will use the App's installation
token for ALL GitHub operations (opening PRs, posting comments, reactions,
labels, merges). PRs and comments will appear as "Sentinel[bot]" instead of
the PAT owner.

If the App token call fails or the App is not configured, Sentinel silently
falls back to the PAT (SENTINEL_GITHUB_TOKEN).
`)
}

// appSetupOptions captures the flags accepted by `app setup`.
type appSetupOptions struct {
	githubHost string
	webhookURL string
	setupURL   string
}

// parseAppSetupArgs scans args for --github-host, --webhook-url, --setup-url,
// and --help/-h. Returns the parsed options and a "helpRequested" flag.
func parseAppSetupArgs(args []string) (appSetupOptions, bool) {
	opts := appSetupOptions{githubHost: "github.com"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--github-host":
			if i+1 < len(args) {
				opts.githubHost = args[i+1]
				i++
			}
		case "--webhook-url":
			if i+1 < len(args) {
				opts.webhookURL = args[i+1]
				i++
			}
		case "--setup-url":
			if i+1 < len(args) {
				opts.setupURL = args[i+1]
				i++
			}
		case "--help", "-h":
			return opts, true
		}
	}
	return opts, false
}

// handleAppSetup runs the manifest flow end-to-end. The user-facing steps
// are printed to stdout; errors abort the flow with os.Exit(1).
func (c *Client) handleAppSetup(args []string) {
	opts, help := parseAppSetupArgs(args)
	if help {
		printAppHelp()
		return
	}
	if err := c.runManifestFlow(context.Background(), os.Stdout, opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runManifestFlow is the testable core of `app setup`. It does NOT call
// os.Exit — it returns errors so callers (and tests) can handle them.
func (c *Client) runManifestFlow(ctx context.Context, w io.Writer, opts appSetupOptions) error {
	port, err := freePort()
	if err != nil {
		return fmt.Errorf("find free port: %w", err)
	}

	callbackURL := fmt.Sprintf("http://localhost:%d/callback", port)
	if csURL, ok := codespacesCallbackURL(port); ok {
		callbackURL = csURL
		fmt.Fprintf(w, "Codespaces detected — using forwarded URL for callback:\n  %s\n\n", csURL)
	}

	setupURL := opts.setupURL
	if setupURL == "" {
		setupURL = c.baseURL + "/settings/github-app"
	}

	manifest := appprovision.DefaultManifest(opts.webhookURL, callbackURL, setupURL,
		"https://github.com/karimzakzouk/sentinel")
	createURL, err := appprovision.CreateAppURL(opts.githubHost, manifest)
	if err != nil {
		return fmt.Errorf("build manifest URL: %w", err)
	}

	fmt.Fprintln(w, "╔══════════════════════════════════════════════════════════════╗")
	fmt.Fprintln(w, "║  Sentinel GitHub App Setup                                   ║")
	fmt.Fprintln(w, "╚══════════════════════════════════════════════════════════════╝")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Step 1 of 3: Create the GitHub App")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Opening your browser to GitHub. You'll see a pre-filled form.")
	fmt.Fprintln(w, "Click \"Create GitHub App\" — all permissions are already configured.")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "If your browser doesn't open, visit this URL manually:\n\n  %s\n\n", createURL)
	fmt.Fprintln(w, "Waiting for you to click Create on GitHub... (Ctrl+C to cancel)")
	fmt.Fprintln(w)
	openBrowser(createURL)

	code, err := waitForCallback(port)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "✓ Received code from GitHub")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Step 2 of 3: Exchanging code for App credentials...")
	appCreds, err := exchangeCodeForApp(ctx, opts.githubHost, code)
	if err != nil {
		fmt.Fprintln(w, "  The code is single-use. Run `sentinelctl app setup` again.")
		return fmt.Errorf("exchange code: %w", err)
	}
	fmt.Fprintf(w, "✓ GitHub App created: %q (App ID: %d, slug: %s)\n\n", appCreds.Name, appCreds.ID, appCreds.Slug)

	fmt.Fprintln(w, "Step 3 of 3: Sending credentials to Sentinel...")
	if err := c.postAppCredentials(ctx, appCreds, opts.webhookURL); err != nil {
		fmt.Fprintf(w, "  Sentinel must be reachable at %s\n", c.baseURL)
		return fmt.Errorf("send credentials: %w", err)
	}
	fmt.Fprintf(w, "✓ Sentinel stored the App credentials. PRs will appear as %s[bot].\n\n", appCreds.Slug)

	installURL := fmt.Sprintf("https://%s/apps/%s/installations/new", opts.githubHost, appCreds.Slug)
	fmt.Fprintln(w, "Next step: install the App on your repo.")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Opening install URL in your browser:\n\n  %s\n\n", installURL)
	fmt.Fprintln(w, "After you click Install, look at the URL in your browser:")
	fmt.Fprintln(w, "  https://github.com/settings/installations/<INSTALL_ID>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Copy the <INSTALL_ID> number and run:")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  sentinelctl app set-install <INSTALL_ID>\n\n")
	openBrowser(installURL)
	return nil
}

// freePort returns a TCP port that is free for binding on 127.0.0.1.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("listener.Addr() returned %T, not *net.TCPAddr", l.Addr())
	}
	return addr.Port, nil
}

// codespacesCallbackURL returns the port-forwarded callback URL when running
// inside a GitHub Codespace, and a bool indicating whether the Codespace
// environment was detected. Also attempts to make the port public via the
// `gh` CLI so GitHub's manifest redirect reaches the Codespace.
func codespacesCallbackURL(port int) (string, bool) {
	if os.Getenv("CODESPACES") != "true" {
		return "", false
	}
	name := os.Getenv("CODESPACE_NAME")
	domain := os.Getenv("GITHUB_CODESPACES_PORT_FORWARDING_DOMAIN")
	if name == "" || domain == "" {
		return "", false
	}
	url := fmt.Sprintf("https://%s-%d.%s/callback", name, port, domain)
	if err := exec.Command("gh", "codespace", "ports", "visibility",
		fmt.Sprintf("%d:public", port), "-c", name).Run(); err != nil {
		fmt.Printf("⚠️  Could not auto-set port visibility (gh CLI not available or not authed).\n")
		fmt.Printf("    Manually make port %d public:\n", port)
		fmt.Printf("    In VS Code: Ports tab → right-click port %d → Port Visibility → Public\n\n", port)
	} else {
		fmt.Printf("✓ Port %d set to public visibility\n\n", port)
	}
	return url, true
}

// waitForCallback starts a localhost HTTP server on the given port and blocks
// until GitHub redirects to /callback?code=<code> or 5 minutes elapse. The
// server responds with a small HTML success page so the user sees a clean
// "App created" tab in their browser.
func waitForCallback(port int) (string, error) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code parameter", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintln(w, `<html><body style="font-family:monospace;padding:40px;background:#0A0A0B;color:#06B6D4"><h1>✓ App created</h1><p>Sentinel received the App credentials. You can close this tab and return to your terminal.</p></body></html>`)
		codeCh <- code
	})}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", fmt.Errorf("bind callback port: %w", err)
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("callback server panic: %v", r)
			}
		}()
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	select {
	case code := <-codeCh:
		return code, nil
	case err := <-errCh:
		return "", fmt.Errorf("callback server failed: %w", err)
	case <-time.After(5 * time.Minute):
		return "", errors.New("timed out waiting for GitHub callback (5 min) — run `sentinelctl app setup` again")
	}
}

// exchangeCodeForApp POSTs the code to GitHub's
// /app-manifests/{code}/conversions endpoint and returns the App credentials.
func exchangeCodeForApp(ctx context.Context, githubHost, code string) (*appprovision.CodeExchangeResponse, error) {
	apiURL := fmt.Sprintf("https://%s/app-manifests/%s/conversions", githubHost, url.PathEscape(code))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call github: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("github returned %d: %s", resp.StatusCode, string(body))
	}
	var result appprovision.CodeExchangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if result.PEM == "" {
		return nil, errors.New("github returned empty PEM")
	}
	return &result, nil
}

// postAppCredentials POSTs the App credentials to Sentinel's
// /settings/github-app endpoint. The install ID is sent separately via
// `sentinelctl app set-install` (or set later via the same endpoint).
func (c *Client) postAppCredentials(ctx context.Context, creds *appprovision.CodeExchangeResponse, webhookURL string) error {
	payload := map[string]any{
		"app_id":         fmt.Sprintf("%d", creds.ID),
		"private_key":    creds.PEM,
		"webhook_secret": creds.WebhookSecret,
		"webhook_url":    webhookURL,
	}
	body, _ := json.Marshal(payload)
	resp, err := c.DoRaw(ctx, http.MethodPost, "/settings/github-app", body, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("sentinel returned 401 — pass --token <SENTINEL_API_TOKEN>")
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("sentinel returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// handleAppSetInstall sets the installation ID after the user installs the
// App on their repo.
func (c *Client) handleAppSetInstall(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: sentinelctl app set-install <install-id>")
		fmt.Fprintln(os.Stderr, "Get the install ID from the URL after installing the App:")
		fmt.Fprintln(os.Stderr, "  https://github.com/settings/installations/<ID>")
		os.Exit(1)
	}
	installID := args[0]
	if err := c.SetAppInstall(context.Background(), os.Stdout, installID); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// SetAppInstall POSTs the install ID to /settings/github-app-install.
func (c *Client) SetAppInstall(ctx context.Context, w io.Writer, installID string) error {
	body, _ := json.Marshal(map[string]string{"install_id": installID})
	resp, err := c.DoRaw(ctx, http.MethodPost, "/settings/github-app-install", body, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("sentinel returned %d: %s", resp.StatusCode, string(respBody))
	}
	fmt.Fprintf(w, "✓ Installation ID set: %s\n", installID)
	fmt.Fprintln(w, "✓ Sentinel is now using the GitHub App for all operations.")
	fmt.Fprintln(w, "✓ PRs + comments will appear as your App's bot identity.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "To verify: trigger an incident and check the PR author.")
	return nil
}

// handleAppStatus shows whether the GitHub App is configured.
func (c *Client) handleAppStatus() {
	if err := c.AppStatus(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// AppStatus GETs /settings/github-app and writes the config state to w.
func (c *Client) AppStatus(ctx context.Context, w io.Writer) error {
	resp, err := c.DoRaw(ctx, http.MethodGet, "/settings/github-app", nil, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		fmt.Fprintln(w, "GitHub App: NOT configured (using PAT — PRs appear as the PAT owner)")
		fmt.Fprintln(w, "Run `sentinelctl app setup` to configure.")
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("sentinel returned %d: %s", resp.StatusCode, string(respBody))
	}
	var status struct {
		Configured bool   `json:"configured"`
		AppID      string `json:"app_id"`
		Slug       string `json:"slug"`
		InstallID  string `json:"install_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !status.Configured {
		fmt.Fprintln(w, "GitHub App: NOT configured (using PAT)")
		return nil
	}
	fmt.Fprintln(w, "GitHub App: configured ✓")
	fmt.Fprintf(w, "  App ID:        %s\n", status.AppID)
	if status.Slug != "" {
		fmt.Fprintf(w, "  Bot identity:  %s[bot]\n", status.Slug)
	}
	fmt.Fprintf(w, "  Install ID:    %s\n", status.InstallID)
	return nil
}

// handleAppRemove removes the GitHub App config (falls back to PAT).
func (c *Client) handleAppRemove() {
	if err := c.AppRemove(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// AppRemove DELETEs the GitHub App config at /settings/github-app.
func (c *Client) AppRemove(ctx context.Context, w io.Writer) error {
	resp, err := c.DoRaw(ctx, http.MethodDelete, "/settings/github-app", nil, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("sentinel returned %d: %s", resp.StatusCode, string(respBody))
	}
	fmt.Fprintln(w, "✓ GitHub App config removed. Sentinel will use the PAT.")
	return nil
}

// openBrowser opens the default browser to the given URL. On Linux it tries
// xdg-open, then wslview (WSL); on macOS it uses `open`; on Windows it uses
// rundll32. Failures are silent — the URL has already been printed to the
// terminal so the user can open it manually.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if _, err := exec.LookPath("xdg-open"); err == nil {
			cmd = exec.Command("xdg-open", url)
		} else if _, err := exec.LookPath("wslview"); err == nil {
			cmd = exec.Command("wslview", url)
		}
	}
	if cmd != nil {
		_ = cmd.Start()
	}
}
