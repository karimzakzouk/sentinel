// Package main — sentinelctl `app` subcommand.
//
// v4.1.0: Implements `sentinelctl app setup` — the one-command GitHub App
// auto-provisioning flow.
//
// Usage:
//   sentinelctl app setup --server http://sentinel:8000 [--token <api-token>] \
//     [--github-host github.com] [--webhook-url <sentinel-webhook-url>]
//
// What it does:
//   1. Spins up a localhost HTTP server on a random port.
//   2. Builds a GitHub App manifest with all permissions pre-configured.
//   3. Opens the user's browser to https://github.com/settings/apps/new?manifest=<base64>
//   4. User clicks "Create GitHub App" on GitHub (form is pre-filled).
//   5. GitHub redirects to http://localhost:<port>/callback?code=<code>
//   6. CLI POSTs the code to GitHub's /app-manifests/{code}/conversions endpoint.
//   7. GitHub returns { id, pem, webhook_secret, ... }
//   8. CLI tells the user to install the App (opens the install URL).
//   9. After install, user runs `sentinelctl app set-install <id>` with the
//      install ID from the install URL.
//  10. CLI POSTs the App ID + PEM + install ID to Sentinel's /settings/github-app
//      endpoint, which wires the App into all GitHub clients live (no restart).
//
// Total user effort: 2 clicks + 1 copy-paste. Total time: ~30 seconds.
//
// Why the install ID is a separate step: GitHub's manifest flow doesn't
// auto-install the App. The user has to visit the install URL, pick a repo,
// and the install ID is in the URL after install. We could auto-detect this
// via the `installation` webhook event (when Sentinel has a public webhook URL),
// but for local-dev / air-gapped setups, the manual paste is the only way.
package main

import (
        "bytes"
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
        "strings"
        "time"

        "github.com/karimzakzouk/sentinel/v3/internal/chatops/appprovision"
        "github.com/karimzakzouk/sentinel/v3/internal/version"
)

func (c *client) handleApp(positional []string) {
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
                fmt.Fprintf(os.Stderr, "Unknown app subcommand: %s\n\n", positional[0])
                printAppHelp()
                os.Exit(1)
        }
}

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

// handleAppSetup runs the manifest flow:
// 1. Spin up localhost HTTP server
// 2. Open browser to GitHub's manifest URL
// 3. Receive the code callback
// 4. Exchange code for App credentials
// 5. POST credentials to Sentinel
// 6. Open the install URL in the browser
func (c *client) handleAppSetup(args []string) {
        githubHost := "github.com"
        webhookURL := ""
        setupURL := ""

        for i := 0; i < len(args); i++ {
                switch args[i] {
                case "--github-host":
                        if i+1 < len(args) {
                                githubHost = args[i+1]
                                i++
                        }
                case "--webhook-url":
                        if i+1 < len(args) {
                                webhookURL = args[i+1]
                                i++
                        }
                case "--setup-url":
                        if i+1 < len(args) {
                                setupURL = args[i+1]
                                i++
                        }
                case "--help", "-h":
                        printAppHelp()
                        return
                }
        }

        // Find a free port for the localhost callback server.
        listener, err := net.Listen("tcp", "127.0.0.1:0")
        if err != nil {
                fmt.Fprintf(os.Stderr, "Failed to start localhost callback server: %v\n", err)
                os.Exit(1)
        }
        tcpAddr, ok := listener.Addr().(*net.TCPAddr)
        if !ok {
                fmt.Fprintf(os.Stderr, "Internal error: listener.Addr() returned non-TCPAddr: %T\n", listener.Addr())
                os.Exit(1)
        }
        port := tcpAddr.Port
        callbackURL := fmt.Sprintf("http://localhost:%d/callback", port)
        if err := listener.Close(); err != nil {
                fmt.Fprintf(os.Stderr, "Warning: failed to close temporary listener: %v\n", err)
        }

        if setupURL == "" {
                setupURL = c.baseURL + "/settings/github-app"
        }

        manifest := appprovision.DefaultManifest(webhookURL, callbackURL, setupURL, "https://github.com/karimzakzouk/sentinel")
        createURL, err := appprovision.CreateAppURL(githubHost, manifest)
        if err != nil {
                fmt.Fprintf(os.Stderr, "Failed to build manifest URL: %v\n", err)
                os.Exit(1)
        }

        fmt.Println("╔══════════════════════════════════════════════════════════════╗")
        fmt.Println("║  Sentinel GitHub App Setup                                   ║")
        fmt.Println("╚══════════════════════════════════════════════════════════════╝")
        fmt.Println()
        fmt.Println("Step 1 of 3: Create the GitHub App")
        fmt.Println()
        fmt.Println("Opening your browser to GitHub. You'll see a pre-filled form.")
        fmt.Println("Click \"Create GitHub App\" — all permissions are already configured.")
        fmt.Println()
        fmt.Printf("If your browser doesn't open, visit this URL manually:\n\n  %s\n\n", createURL)
        fmt.Println("Waiting for you to click Create on GitHub... (Ctrl+C to cancel)")
        fmt.Println()

        // Open the browser.
        openBrowser(createURL)

        // Start the callback server.
        codeCh := make(chan string, 1)
        errCh := make(chan error, 1)
        srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                if r.URL.Path != "/callback" {
                        http.NotFound(w, r)
                        return
                }
                code := r.URL.Query().Get("code")
                if code == "" {
                        w.WriteHeader(http.StatusBadRequest)
                        fmt.Fprintln(w, "missing code parameter")
                        return
                }
                // Show a success page to the user.
                w.Header().Set("Content-Type", "text/html")
                w.WriteHeader(http.StatusOK)
                fmt.Fprintln(w, `<html><body style="font-family:monospace;padding:40px;background:#0A0A0B;color:#06B6D4"><h1>✓ App created</h1><p>Sentinel received the App credentials. You can close this tab and return to your terminal.</p></body></html>`)
                codeCh <- code
        })}

        go func() {
                if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
                        errCh <- err
                }
        }()
        defer func() {
                ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
                defer cancel()
                _ = srv.Shutdown(ctx)
        }()

        // Need to re-listen on the same port (we closed the listener above to find a free port).
        // Re-create the listener.
        listener, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
        if err != nil {
                fmt.Fprintf(os.Stderr, "Failed to bind callback port: %v\n", err)
                os.Exit(1)
        }
        go func() {
                _ = srv.Serve(listener)
        }()

        // Wait for the callback (timeout 5 min).
        select {
        case code := <-codeCh:
                fmt.Println("✓ Received code from GitHub")
                fmt.Println()
                fmt.Println("Step 2 of 3: Exchanging code for App credentials...")
                appCreds, err := exchangeCodeForApp(githubHost, code)
                if err != nil {
                        fmt.Fprintf(os.Stderr, "✗ Failed to exchange code: %v\n", err)
                        fmt.Fprintf(os.Stderr, "  The code is single-use. Run `sentinelctl app setup` again.\n")
                        os.Exit(1)
                }
                fmt.Printf("✓ GitHub App created: %q (App ID: %d, slug: %s)\n", appCreds.Name, appCreds.ID, appCreds.Slug)
                fmt.Println()

                fmt.Println("Step 3 of 3: Sending credentials to Sentinel...")
                if err := c.postAppCredentials(appCreds, webhookURL); err != nil {
                        fmt.Fprintf(os.Stderr, "✗ Failed to send credentials to Sentinel: %v\n", err)
                        fmt.Fprintf(os.Stderr, "  Sentinel must be reachable at %s\n", c.baseURL)
                        os.Exit(1)
                }
                fmt.Printf("✓ Sentinel stored the App credentials. PRs will appear as %s[bot].\n", appCreds.Slug)
                fmt.Println()

                // Now open the install URL so the user can install the App on their repo.
                installURL := fmt.Sprintf("https://%s/apps/%s/installations/new", githubHost, appCreds.Slug)
                fmt.Println("Next step: install the App on your repo.")
                fmt.Println()
                fmt.Printf("Opening install URL in your browser:\n\n  %s\n\n", installURL)
                fmt.Println("After you click Install, look at the URL in your browser:")
                fmt.Println("  https://github.com/settings/installations/<INSTALL_ID>")
                fmt.Println()
                fmt.Println("Copy the <INSTALL_ID> number and run:")
                fmt.Println()
                fmt.Printf("  sentinelctl app set-install <INSTALL_ID>\n\n")
                openBrowser(installURL)
                return
        case err := <-errCh:
                fmt.Fprintf(os.Stderr, "✗ Callback server failed: %v\n", err)
                os.Exit(1)
        case <-time.After(5 * time.Minute):
                fmt.Fprintln(os.Stderr, "✗ Timed out waiting for GitHub callback (5 min).")
                fmt.Fprintln(os.Stderr, "  Run `sentinelctl app setup` again.")
                os.Exit(1)
        }
}

// exchangeCodeForApp POSTs the code to GitHub's /app-manifests/{code}/conversions
// endpoint and returns the App credentials.
func exchangeCodeForApp(githubHost, code string) (*appprovision.CodeExchangeResponse, error) {
        apiURL := fmt.Sprintf("https://%s/app-manifests/%s/conversions", githubHost, url.PathEscape(code))
        req, err := http.NewRequest("POST", apiURL, nil)
        if err != nil {
                return nil, err
        }
        req.Header.Set("Accept", "application/vnd.github+json")
        resp, err := http.DefaultClient.Do(req)
        if err != nil {
                return nil, err
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

// postAppCredentials POSTs the App credentials to Sentinel's /settings/github-app endpoint.
func (c *client) postAppCredentials(creds *appprovision.CodeExchangeResponse, webhookURL string) error {
        payload := map[string]any{
                "app_id":          fmt.Sprintf("%d", creds.ID),
                "private_key":     creds.PEM,
                "webhook_secret":  creds.WebhookSecret,
                "webhook_url":     webhookURL,
                // install_id is set later via `sentinelctl app set-install`
        }
        body, _ := json.Marshal(payload)
        req, err := http.NewRequest("POST", c.baseURL+"/settings/github-app", bytes.NewReader(body))
        if err != nil {
                return err
        }
        req.Header.Set("Content-Type", "application/json")
        if c.token != "" {
                req.Header.Set("Authorization", "Bearer "+c.token)
        }
        resp, err := c.http.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                if resp.StatusCode == http.StatusUnauthorized {
                        return fmt.Errorf("sentinel returned 401 — pass --token <SENTINEL_API_TOKEN>")
                }
                return fmt.Errorf("sentinel returned %d: %s", resp.StatusCode, string(body))
        }
        return nil
}

// handleAppSetInstall sets the installation ID after the user installs the App.
// The install ID is in the URL after install:
//   https://github.com/settings/installations/<ID>
func (c *client) handleAppSetInstall(args []string) {
        if len(args) == 0 {
                fmt.Fprintln(os.Stderr, "Usage: sentinelctl app set-install <install-id>")
                fmt.Fprintln(os.Stderr, "Get the install ID from the URL after installing the App:")
                fmt.Fprintln(os.Stderr, "  https://github.com/settings/installations/<ID>")
                os.Exit(1)
        }
        installID := args[0]
        payload := map[string]string{"install_id": installID}
        body, _ := json.Marshal(payload)
        req, err := http.NewRequest("POST", c.baseURL+"/settings/github-app-install", bytes.NewReader(body))
        if err != nil {
                fmt.Fprintf(os.Stderr, "build request: %v\n", err)
                os.Exit(1)
        }
        req.Header.Set("Content-Type", "application/json")
        if c.token != "" {
                req.Header.Set("Authorization", "Bearer "+c.token)
        }
        resp, err := c.http.Do(req)
        if err != nil {
                fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
                os.Exit(1)
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                fmt.Fprintf(os.Stderr, "sentinel returned %d: %s\n", resp.StatusCode, string(body))
                os.Exit(1)
        }
        fmt.Printf("✓ Installation ID set: %s\n", installID)
        fmt.Println("✓ Sentinel is now using the GitHub App for all operations.")
        fmt.Println("✓ PRs + comments will appear as your App's bot identity.")
        fmt.Println()
        fmt.Println("To verify: trigger an incident and check the PR author.")
}

// handleAppStatus shows whether the GitHub App is configured.
func (c *client) handleAppStatus() {
        req, err := http.NewRequest("GET", c.baseURL+"/settings/github-app", nil)
        if err != nil {
                fmt.Fprintf(os.Stderr, "build request: %v\n", err)
                os.Exit(1)
        }
        if c.token != "" {
                req.Header.Set("Authorization", "Bearer "+c.token)
        }
        resp, err := c.http.Do(req)
        if err != nil {
                fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
                os.Exit(1)
        }
        defer resp.Body.Close()
        if resp.StatusCode == http.StatusNotFound {
                fmt.Println("GitHub App: NOT configured (using PAT — PRs appear as the PAT owner)")
                fmt.Println("Run `sentinelctl app setup` to configure.")
                return
        }
        if resp.StatusCode != http.StatusOK {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                fmt.Fprintf(os.Stderr, "sentinel returned %d: %s\n", resp.StatusCode, string(body))
                os.Exit(1)
        }
        var status struct {
                Configured bool   `json:"configured"`
                AppID      string `json:"app_id"`
                Slug       string `json:"slug"`
                InstallID  string `json:"install_id"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
                fmt.Fprintf(os.Stderr, "decode response: %v\n", err)
                os.Exit(1)
        }
        if !status.Configured {
                fmt.Println("GitHub App: NOT configured (using PAT)")
                return
        }
        fmt.Printf("GitHub App: configured ✓\n")
        fmt.Printf("  App ID:        %s\n", status.AppID)
        if status.Slug != "" {
                fmt.Printf("  Bot identity:  %s[bot]\n", status.Slug)
        }
        fmt.Printf("  Install ID:    %s\n", status.InstallID)
}

// handleAppRemove removes the GitHub App config (falls back to PAT).
func (c *client) handleAppRemove() {
        req, err := http.NewRequest("DELETE", c.baseURL+"/settings/github-app", nil)
        if err != nil {
                fmt.Fprintf(os.Stderr, "build request: %v\n", err)
                os.Exit(1)
        }
        if c.token != "" {
                req.Header.Set("Authorization", "Bearer "+c.token)
        }
        resp, err := c.http.Do(req)
        if err != nil {
                fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
                os.Exit(1)
        }
        defer resp.Body.Close()
        if resp.StatusCode != http.StatusOK {
                body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
                fmt.Fprintf(os.Stderr, "sentinel returned %d: %s\n", resp.StatusCode, string(body))
                os.Exit(1)
        }
        fmt.Println("✓ GitHub App config removed. Sentinel will use the PAT.")
}

// openBrowser opens the default browser to the given URL.
func openBrowser(url string) {
        var cmd *exec.Cmd
        switch runtime.GOOS {
        case "darwin":
                cmd = exec.Command("open", url)
        case "windows":
                cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
        default: // linux, etc.
                // Try xdg-open, fall back silently if it's not installed.
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

// version import guard
var _ = version.Version
var _ = strings.TrimSpace
