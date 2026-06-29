// Package sandbox defines the abstraction over isolation runtimes where
// Sentinel verifies proposed fixes.
//
// A Backend is an isolation runtime that can deploy a candidate image and
// verify it under controlled conditions. The default implementation is
// K8sNamespaceBackend (deploys a canary pod in a fresh K8s namespace).
// Future backends: gVisor, Firecracker, Docker-local, AWS ECS task.
//
// To add a new backend:
//   1. Implement the Backend interface in a new file under internal/sandbox/
//   2. Wire it up in cmd/sentinel/main.go (or wherever SandboxRunner is constructed)
//
// The SandboxRunner orchestrator (in internal/executors) is backend-agnostic
// — it calls Backend.Deploy + Backend.Verify and feeds the result back to
// the LLM. It does not know whether the backend is a K8s namespace, a
// gVisor sandbox, or a Firecracker microVM.
package sandbox

import (
	"context"
	"time"
)

// Backend is an isolation runtime that can deploy a candidate image and
// verify it under controlled conditions.
//
// Lifecycle (called by SandboxRunner):
//   1. Prepare(ctx, inc) → creates the sandbox env (namespace, microVM, etc.)
//   2. Deploy(ctx, inc, image) → deploys the candidate image, returns deploy info
//   3. Verify(ctx, inc, info) → runs the 5-level verification suite, returns result
//   4. Logs(ctx, info) → fetches sandbox pod/container logs (for LLM feedback)
//   5. Cleanup(ctx, info) → tears down the sandbox env
//
// All methods must be safe to call from a goroutine. The SandboxRunner
// orchestrator handles retries + LLM feedback loops; the Backend just
// executes the requested phase.
type Backend interface {
	// Name returns a stable identifier (e.g. "k8s_namespace", "gvisor",
	// "firecracker"). Used in metrics + logs.
	Name() string

	// Prepare creates the sandbox environment (namespace, microVM, container,
	// etc.) and returns a SandboxInfo handle that subsequent calls use.
	// The info must include a stable identifier so Cleanup can find it later.
	Prepare(ctx context.Context, req PrepareRequest) (*SandboxInfo, error)

	// Deploy applies the candidate image to the sandbox env. Returns updated
	// SandboxInfo with deploy-specific fields populated (e.g. pod name).
	Deploy(ctx context.Context, info *SandboxInfo, image string) error

	// Verify runs the verification suite and returns the result. The result
	// must include per-level pass/fail, logs, and whether the failure is
	// infrastructure (RBAC, image pull) vs. application (LLM can fix).
	Verify(ctx context.Context, info *SandboxInfo, req VerifyRequest) VerifyResult

	// Logs fetches the most recent sandbox pod/container logs. Used to feed
	// failure context back to the LLM for the next retry iteration.
	Logs(ctx context.Context, info *SandboxInfo) (string, error)

	// Cleanup tears down the sandbox env. Must be idempotent — if the env is
	// already gone, return nil.
	Cleanup(ctx context.Context, info *SandboxInfo) error

	// ReapStale removes any sandbox envs older than maxAge. Called periodically
	// by a background goroutine to clean up after crashed investigations.
	ReapStale(ctx context.Context, maxAge time.Duration) int
}

// PrepareRequest carries the inputs needed to set up a sandbox env.
type PrepareRequest struct {
	// Service is the affected service name (e.g. "payments-api").
	Service string

	// Namespace is the prod namespace the service runs in (used to clone
	// the Deployment spec, NetworkPolicy, etc.).
	Namespace string

	// Repo is the GitHub repo (owner/name) the fix comes from.
	Repo string

	// Branch is the Git branch the fix lives on.
	Branch string

	// IncidentID is the UUID of the incident being verified.
	IncidentID string
}

// SandboxInfo is the handle returned by Prepare and passed to subsequent calls.
// Backends should populate the fields they support; consumers must tolerate
// empty fields for backends that don't use them.
type SandboxInfo struct {
	// BackendName is the name of the backend that created this info.
	BackendName string

	// SandboxID is a stable identifier for the sandbox env (e.g. K8s namespace
	// name, container ID, microVM ID).
	SandboxID string

	// PodName is the canary pod name (K8s backends only).
	PodName string

	// CreatedAt is when the sandbox env was created.
	CreatedAt time.Time

	// BackendData is opaque backend-specific data (e.g. JSON-encoded state).
	// Backends can use this to carry anything they need between calls.
	BackendData []byte
}

// VerifyRequest carries verification parameters.
type VerifyRequest struct {
	// ProbePaths is the list of HTTP paths to probe (e.g. ["/", "/healthz", "/readyz"]).
	ProbePaths []string

	// SustainedReady is how long the canary must stay Ready before passing
	// Level 1.
	SustainedReady time.Duration

	// ErrorPatterns is the list of error strings to scan canary logs for
	// (Level 3 — "original error gone" check).
	ErrorPatterns []string

	// RunRegressionTest: if true, ask the LLM to generate a test that
	// reproduces the original error, then run it against the canary. If it
	// fails, the fix didn't actually fix the bug.
	RunRegressionTest bool

	// RunReproducer: if true, ask the LLM to generate a reproducer script
	// and run it. Strongest verification (Level 5).
	RunReproducer bool
}

// VerifyResult is the outcome of a sandbox verification run.
type VerifyResult struct {
	// Verified is true if all enabled levels passed.
	Verified bool

	// Summary is a one-line human-readable result (e.g. "Level 1+2+3 passed,
	// error gone in 47s").
	Summary string

	// Level1Passed: canary pod became Ready AND stayed Ready for SustainedReady.
	Level1Passed bool

	// Level2Passed: all ProbePaths returned 2xx/3xx.
	Level2Passed bool

	// Level3Passed: original error pattern not found in canary logs.
	Level3Passed bool

	// Level4Passed: regression test (LLM-generated) passed.
	Level4Passed bool

	// Level5Passed: reproducer script (LLM-generated) ran without error.
	Level5Passed bool

	// InfrastructureError is true if the failure was infra (RBAC denied,
	// image pull failure, namespace creation failed). When true, the retry
	// loop should NOT feed the error back to the LLM — the LLM can't fix
	// a permissions issue.
	InfrastructureError bool

	// PodLogs is the last N bytes of canary pod logs (for Level 1 failure).
	PodLogs string

	// ProbeResults is per-probe outcome (for Level 2/3 failure).
	ProbeResults []string

	// ErrorCount5xx is the number of 5xx responses seen during probing.
	ErrorCount5xx int

	// RequestCount is the total number of probe requests sent.
	RequestCount int

	// MirrorApplied is true if traffic mirroring was used (Tier 1+2 depth).
	MirrorApplied bool

	// Duration is how long the verification took.
	Duration time.Duration
}

// IsInfraFailure returns true if the failure is infrastructure (RBAC, image
// pull, namespace creation) — i.e. not something the LLM can fix.
func (r VerifyResult) IsInfraFailure() bool {
	return r.InfrastructureError
}

// PassedLevels returns the list of levels that passed (e.g. "1,2,3").
func (r VerifyResult) PassedLevels() string {
	levels := ""
	if r.Level1Passed {
		levels += "1,"
	}
	if r.Level2Passed {
		levels += "2,"
	}
	if r.Level3Passed {
		levels += "3,"
	}
	if r.Level4Passed {
		levels += "4,"
	}
	if r.Level5Passed {
		levels += "5,"
	}
	if levels == "" {
		return "none"
	}
	return levels[:len(levels)-1] // strip trailing comma
}
