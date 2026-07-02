package sandbox

import (
	"testing"
	"time"
)

func TestVerifyResultPassedLevels(t *testing.T) {
	tests := []struct {
		name    string
		result  VerifyResult
		want    string
	}{
		{
			name: "none passed",
			result: VerifyResult{},
			want: "none",
		},
		{
			name: "L1-L3 passed",
			result: VerifyResult{
				Level1Passed: true,
				Level2Passed: true,
				Level3Passed: true,
			},
			want: "1,2,3",
		},
		{
			name: "all passed",
			result: VerifyResult{
				Level1Passed: true,
				Level2Passed: true,
				Level3Passed: true,
				Level4Passed: true,
				Level5Passed: true,
			},
			want: "1,2,3,4,5",
		},
		{
			name: "only L4-L5",
			result: VerifyResult{
				Level4Passed: true,
				Level5Passed: true,
			},
			want: "4,5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.PassedLevels()
			if got != tt.want {
				t.Errorf("PassedLevels() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerifyResultIsInfraFailure(t *testing.T) {
	r := VerifyResult{InfrastructureError: true}
	if !r.IsInfraFailure() {
		t.Error("expected IsInfraFailure to be true when InfrastructureError is set")
	}

	r2 := VerifyResult{InfrastructureError: false}
	if r2.IsInfraFailure() {
		t.Error("expected IsInfraFailure to be false when InfrastructureError is not set")
	}
}

func TestSandboxInfoFields(t *testing.T) {
	info := &SandboxInfo{
		BackendName: "k8s_namespace",
		SandboxID:   "sentinel-sandbox-test-abc123",
		PodName:     "canary-pod",
		CreatedAt:   time.Now(),
	}

	if info.BackendName != "k8s_namespace" {
		t.Errorf("BackendName: got %q", info.BackendName)
	}
	if info.SandboxID == "" {
		t.Error("SandboxID should not be empty")
	}
}

func TestPrepareRequest(t *testing.T) {
	req := PrepareRequest{
		Service:    "payments-api",
		Namespace:  "production",
		Repo:       "org/payments",
		Branch:     "sentinel/fix-abc123",
		IncidentID: "550e8400-e29b-41d4-a716-446655440000",
	}

	if req.Service != "payments-api" {
		t.Errorf("Service: got %q", req.Service)
	}
	if req.IncidentID == "" {
		t.Error("IncidentID should not be empty")
	}
}

func TestVerifyRequestDefaults(t *testing.T) {
	req := VerifyRequest{
		ProbePaths:    []string{"/", "/healthz"},
		SustainedReady: 30 * time.Second,
		ErrorPatterns:  []string{"KeyError", "TypeError"},
	}

	if len(req.ProbePaths) != 2 {
		t.Errorf("expected 2 probe paths, got %d", len(req.ProbePaths))
	}
	if req.SustainedReady != 30*time.Second {
		t.Errorf("SustainedReady: got %v", req.SustainedReady)
	}
}
