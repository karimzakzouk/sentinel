package executors

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/google/uuid"
	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// failingPolicyClientset wraps the fake clientset so that NetworkPolicy
// creation always fails (simulates missing RBAC / quota / throttling).
func failingPolicyClientset() *fake.Clientset {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "networkpolicies", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "networking.k8s.io", Resource: "networkpolicies"},
			"sandbox-deny", errors.New("rbac: denied"))
	})
	return cs
}

func sandboxIncident() *schemas.Incident {
	fixID := uuid.New()
	return &schemas.Incident{
		IncidentID: uuid.New(),
		State: schemas.AgentState{
			FixProposal: &schemas.FixProposal{
				FixID:           fixID,
				FixType:         schemas.FixCodeFix,
				TargetService:   "api",
				TargetNamespace: "sim",
			},
		},
	}
}

// TestSandboxNetworkPolicyFailClosed verifies the P0 fix: when the
// default-deny NetworkPolicy cannot be created, the run ABORTS with an
// infrastructure error instead of deploying a canary with unrestricted
// egress.
func TestSandboxNetworkPolicyFailClosed(t *testing.T) {
	cs := failingPolicyClientset()
	s := NewSandbox(cs, "sentinel-sandbox", false).
		WithNetworkPolicyRequired(true)

	res := s.Run(context.Background(), sandboxIncident())

	if res.Verified {
		t.Fatal("run must not verify when NetworkPolicy creation fails")
	}
	if !res.InfrastructureError {
		t.Error("failure must be marked as InfrastructureError so the LLM retry loop does not spin")
	}
	if !strings.Contains(res.Summary, "networkpolicy_create_failed") {
		t.Errorf("summary should name the fail-closed cause, got: %s", res.Summary)
	}
}

// TestSandboxNetworkPolicyFailOpenOptIn verifies the escape hatch: with
// SENTINEL_SANDBOX_NETWORK_POLICY_REQUIRED=false the run proceeds past the
// policy failure (fail-open is now an explicit operator choice, not a
// silent default).
func TestSandboxNetworkPolicyFailOpenOptIn(t *testing.T) {
	cs := failingPolicyClientset()
	s := NewSandbox(cs, "sentinel-sandbox", false).
		WithNetworkPolicyRequired(false)

	res := s.Run(context.Background(), sandboxIncident())

	// The run proceeds past the NetworkPolicy failure — it will fail later
	// at canary deployment (no pods in the fake cluster), but NOT with the
	// networkpolicy error. That's the assertion: fail-open continues.
	if strings.Contains(res.Summary, "networkpolicy_create_failed") {
		t.Errorf("fail-open mode must not abort on policy failure, got: %s", res.Summary)
	}
}

// TestSandboxLevelsVerifiedSkipped ensures a failing run never claims
// verified levels.
func TestSandboxLevelsVerifiedSkipped(t *testing.T) {
	cs := fake.NewSimpleClientset()
	s := NewSandbox(cs, "sentinel-sandbox", false).
		WithNetworkPolicyRequired(false)

	res := s.Run(context.Background(), sandboxIncident())

	if res.Verified {
		t.Fatal("fake cluster cannot verify a canary — expected failure")
	}
	if res.LevelsVerified != "" {
		t.Errorf("failed run must not claim verified levels, got %q", res.LevelsVerified)
	}
}
