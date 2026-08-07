// Package appprovision implements the GitHub App manifest flow — a
// one-command setup that creates a GitHub App in the user's account with
// all permissions pre-configured.
//
// Flow:
//
//  1. Sentinel generates a Manifest (DefaultManifest) describing the App:
//     name, webhook URL, default permissions, default events.
//  2. The manifest is base64-encoded and appended to a GitHub URL.
//  3. The user clicks the URL; GitHub pre-fills the "Create GitHub App"
//     form. The user confirms.
//  4. GitHub redirects back to Sentinel with a code.
//  5. Sentinel POSTs the code to /app-manifests/{code}/conversions and
//     receives the App's PEM, ID, client ID/secret, and webhook secret.
//  6. Sentinel stores everything in runtime config — no manual App
//     creation, no PEM file management, no permission dropdowns.
//
// Docs:
// https://docs.github.com/en/apps/sharing-github-apps/sharing-your-github-app-with-other-users
package appprovision
