// Package executors implements Sentinel's execution layer: the actions taken
// once a fix has been proposed, security-scanned, and approved.
//
// Three fix paths converge here:
//
//   - code_fix: the LLM emitted SEARCH/REPLACE blocks or a whole-file
//     replacement. The github_searchreplace applier applies the edits, the
//     Kaniko builder builds a canary image, and the SandboxRunner runs the
//     5-level verification suite before a PR is opened.
//   - k8s_action (restart / scale / rollback): a K8s-only patch is applied
//     directly via the K8sExecutor. No sandbox, no PR.
//   - infrastructure: a background RemediationController polls the cluster
//     for stuck pods and disk pressure, taking low-risk actions (deleting
//     evicted pods, restarting CrashLoopBackOff pods past a high restart
//     count).
//
// The 5-level sandbox verification suite:
//
//  1. Canary pod reaches Ready AND stays Ready for sustainedWindow (15s
//     default). Catches pods that go Ready then crash on first request.
//  2. Health endpoints return 2xx/3xx. Paths come from the prod
//     Deployment's own readiness/liveness probes (spec), the operator's
//     configured list, or a convention candidate set (any-of) — in that
//     precedence. 4xx/5xx fails — a broken health endpoint means the app
//     is broken.
//  3. Original error type is GONE from canary logs (PRIMARY ORACLE). Uses
//     agents.ParseError (multi-language) to identify the original error
//     type from incident anomalies, then scans canary logs for it.
//  4. LLM-generated regression test passes against the canary image.
//  5. LLM-generated reproducer script runs without crashing. L4 and L5
//     run in parallel. L5 distinguishes "infra error" (don't fail) from
//     "real failure" (fail) so a broken test runner doesn't block a
//     correct fix.
//
// Defense-in-depth:
//
//   - Sandbox namespace has a default-deny NetworkPolicy (ingress only
//     from the Sentinel namespace, egress fully denied).
//   - Canary pod spec is hardened: privileged=false, ALL capabilities
//     dropped, allowPrivilegeEscalation=false, seccomp=RuntimeDefault,
//     hostNetwork/hostPID/hostIPC=false, hostPath volumes replaced with
//     emptyDir, readOnlyRootFilesystem=true with /tmp emptyDir mounted.
//   - Kaniko init container validates repo/branch against a strict
//     allowlist before reaching a shell; the GitHub token is sourced
//     from a K8s Secret env var (never interpolated into the script).
//   - Test/reproducer file paths + commands are validated against a
//     known-safe subset before being interpolated into shell commands.
package executors
