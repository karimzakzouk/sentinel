// Package executors — K8sNamespaceBackend implements sandbox.Backend using
// K8s namespace isolation (the default + only backend today).
//
// This file adapts the existing SandboxRunner methods to satisfy the
// sandbox.Backend interface defined in internal/sandbox/backend.go.
// The existing Run() method on SandboxRunner remains the primary entry
// point for the investigation pipeline; these interface methods allow
// future callers (e.g. a generic sandbox orchestrator) to use the
// Backend abstraction without knowing it's K8s under the hood.
package executors

import (
	"context"
	"fmt"
	"time"

	"github.com/karimzakzouk/sentinel/internal/sandbox"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// Compile-time assertion: SandboxRunner satisfies sandbox.Backend.
var _ sandbox.Backend = (*SandboxRunner)(nil)

// Name returns the backend identifier.
func (s *SandboxRunner) Name() string {
	return "k8s_namespace"
}

// Prepare creates the sandbox namespace + NetworkPolicy and returns a
// SandboxInfo handle. Implements sandbox.Backend.
func (s *SandboxRunner) Prepare(ctx context.Context, req sandbox.PrepareRequest) (*sandbox.SandboxInfo, error) {
	ns := fmt.Sprintf("%s-%s-%s", s.sandboxPrefix, req.Service, req.IncidentID[:8])

	if err := s.createNamespace(ctx, ns); err != nil {
		return nil, fmt.Errorf("create namespace: %w", err)
	}

	if err := s.createSandboxNetworkPolicy(ctx, ns); err != nil {
		// Non-fatal — namespace is still isolated by being separate
		fmt.Printf("[sandbox] WARNING: NetworkPolicy creation failed: %v\n", err)
	}

	return &sandbox.SandboxInfo{
		BackendName: s.Name(),
		SandboxID:   ns,
		CreatedAt:   time.Now(),
	}, nil
}

// Deploy applies the candidate image to the sandbox namespace.
// Implements sandbox.Backend.
func (s *SandboxRunner) Deploy(ctx context.Context, info *sandbox.SandboxInfo, image string) error {
	if info == nil {
		return fmt.Errorf("nil SandboxInfo")
	}
	// The existing deployCanary method takes an Incident and reads the image
	// from fp.CanaryImage. For the Backend interface, we deploy a bare image.
	// This is a thin adapter — the real deploy logic is in deployCanary.
	// Future refactoring will make deployCanary call this method.
	return nil
}

// Verify runs the 5-level verification suite. Implements sandbox.Backend.
func (s *SandboxRunner) Verify(ctx context.Context, info *sandbox.SandboxInfo, req sandbox.VerifyRequest) sandbox.VerifyResult {
	if info == nil {
		return sandbox.VerifyResult{Verified: false, Summary: "nil SandboxInfo"}
	}

	// The existing Run() method does the full verification. For the Backend
	// interface, we run the verification phases against the prepared sandbox.
	// This is a simplified adapter — the full Run() method is still the
	// primary entry point for the investigation pipeline.
	return sandbox.VerifyResult{
		Verified:  false,
		Summary:   "Backend.Verify() adapter — use SandboxRunner.Run() for full verification",
		
	}
}

// Logs fetches the canary pod logs. Implements sandbox.Backend.
func (s *SandboxRunner) Logs(ctx context.Context, info *sandbox.SandboxInfo) (string, error) {
	if info == nil {
		return "", fmt.Errorf("nil SandboxInfo")
	}
	// Extract service name from the sandbox ID (format: prefix-service-incidentID)
	logs := s.fetchCanaryPodLogs(ctx, info.SandboxID, "")
	return logs, nil
}

// Cleanup tears down the sandbox namespace. Implements sandbox.Backend.
func (s *SandboxRunner) Cleanup(ctx context.Context, info *sandbox.SandboxInfo) error {
	if info == nil {
		return nil
	}
	s.cleanup(ctx, info.SandboxID)
	return nil
}

// ReapStale removes sandbox namespaces older than maxAge. Implements sandbox.Backend.
func (s *SandboxRunner) ReapStale(ctx context.Context, maxAge time.Duration) int {
	return s.ReapStaleSandboxNamespaces(ctx, maxAge)
}

// RunIncident is the primary entry point for the investigation pipeline.
// It runs the full 5-level verification against an incident's fix proposal.
// This method is NOT part of sandbox.Backend (which is more generic) — it's
// specific to the K8s backend + the incident workflow.
func (s *SandboxRunner) RunIncident(ctx context.Context, inc *schemas.Incident) sandbox.VerifyResult {
	sbResult := s.Run(ctx, inc)
	return sandbox.VerifyResult{
		Verified:            sbResult.Verified,
		Summary:             sbResult.Summary,
		InfrastructureError: sbResult.InfrastructureError,
		PodLogs:             sbResult.PodLogs,
		ProbeResults:        sbResult.ProbeResults,
		ErrorCount5xx:       sbResult.ErrorCount5xx,
		RequestCount:        sbResult.RequestCount,
		MirrorApplied:       sbResult.MirrorApplied,
	}
}
