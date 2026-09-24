package agents

import (
	"strings"
	"testing"

	"github.com/karimzakzouk/sentinel/internal/schemas"
)

// run11UserAPIContext reproduces the runtime context of the user-api pod
// from eval run #11: the deployment sets USER_DATABASE_URL (verified in
// svc/user-api/k8s/deployment.yaml:34), which the v1.7.3 investigation
// never saw — it guessed a canonical DATABASE_URL and proposed a harmful
// rename.
func run11UserAPIContext() *schemas.PodRuntimeContext {
	return &schemas.PodRuntimeContext{
		PodName:       "user-api-86cc7c8bf7-7bqvf",
		ContainerName: "user-api",
		Image:         "ghcr.io/marketly-org/user-api:sha",
		EnvVarNames:   []string{"JWT_SECRET", "NODE_OPTIONS", "PORT", "USER_DATABASE_URL"},
		SecretRefs:    []string{"marketly-secrets"},
		MemoryLimit:   "256Mi",
		CPULimit:      "500m",
		Command:       []string{"node", "dist/server.js"},
	}
}

func TestRenderRuntimeContextNil(t *testing.T) {
	if s := renderRuntimeContext(nil); s != "" {
		t.Errorf("nil context should render empty, got %q", s)
	}
}

func TestRenderRuntimeContextContainsEnvNames(t *testing.T) {
	s := renderRuntimeContext(run11UserAPIContext())
	mustContain := []string{
		"## Pod Runtime Context",
		"USER_DATABASE_URL",
		"NODE_OPTIONS",
		"marketly-secrets",
		"256Mi",
		"user-api-86cc7c8bf7-7bqvf",
		"Never assume a canonical variable name", // anti-rename instruction
	}
	for _, sub := range mustContain {
		if !strings.Contains(s, sub) {
			t.Errorf("rendered context missing %q:\n%s", sub, s)
		}
	}
	// Values must never be rendered (none were set in this context, and
	// the renderer has no path to include them — names and refs only).
	if strings.Contains(s, "postgresql://") {
		t.Error("env var VALUE leaked into rendered context")
	}
}

func TestRenderRuntimeContextEmptyEnv(t *testing.T) {
	s := renderRuntimeContext(&schemas.PodRuntimeContext{PodName: "p", EnvVarNames: nil})
	if !strings.Contains(s, "(none)") {
		t.Errorf("empty env should render '(none)':\n%s", s)
	}
}

func TestRuntimeContextGroundingRules(t *testing.T) {
	if s := runtimeContextGroundingRules(nil); s != "" {
		t.Errorf("nil context should produce no rules, got %q", s)
	}
	if s := runtimeContextGroundingRules(&schemas.PodRuntimeContext{EnvVarNames: []string{}}); s != "" {
		t.Errorf("no env vars should produce no rules, got %q", s)
	}
	s := runtimeContextGroundingRules(run11UserAPIContext())
	if !strings.Contains(s, "Do NOT rename env var references") {
		t.Errorf("grounding rules missing the anti-rename warning:\n%s", s)
	}
}

// TestFixProposerMessageIncludesRuntimeContext asserts the fix proposer's
// user message carries the ground truth — the direct guard against the
// run-#11 harmful fix (USER_DATABASE_URL -> DATABASE_URL rename).
func TestFixProposerMessageIncludesRuntimeContext(t *testing.T) {
	fp := NewFixProposer(nil, nil, "")
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service:   "user-api",
			Namespace: "marketly",
			Summary:   "Repeated container restarts",
		},
		RootCause: &schemas.RootCause{
			Statement:  "pg pool connection timeout in getUserByEmail",
			Category:   "conn_pool_exhausted",
			Confidence: 0.61,
		},
		FetchedCode:    map[string]string{"src/db.ts": "const pool = new Pool({...})"},
		RuntimeContext: run11UserAPIContext(),
	}
	msg := fp.buildUserMessage(state)
	if !strings.Contains(msg, "USER_DATABASE_URL") {
		t.Error("fix proposer message missing USER_DATABASE_URL ground truth")
	}
	if !strings.Contains(msg, "Do NOT rename env var references") {
		t.Error("fix proposer message missing the anti-rename grounding rule")
	}
}

// TestInvestigatorMessagesIncludeRuntimeContext asserts both investigation
// rounds see the ground truth, so wrong env-var hypotheses die at
// diagnosis time rather than at fix time.
func TestInvestigatorMessagesIncludeRuntimeContext(t *testing.T) {
	inv := NewInvestigator(nil, nil, "", nil)
	state := &schemas.AgentState{
		Cluster: schemas.AnomalyCluster{
			Service:   "user-api",
			Namespace: "marketly",
			Summary:   "crash loop",
		},
		RuntimeContext: run11UserAPIContext(),
	}
	fileTree := []string{"src/db.ts", "src/server.ts", "k8s/deployment.yaml"}

	r1 := inv.buildRound1Message(state, "Error: Connection terminated", fileTree)
	if !strings.Contains(r1, "USER_DATABASE_URL") {
		t.Error("round-1 message missing runtime context env names")
	}

	r2 := inv.buildRound2Message(state, "Error: Connection terminated", fileTree, nil)
	if !strings.Contains(r2, "USER_DATABASE_URL") {
		t.Error("round-2 message missing runtime context env names")
	}
}
