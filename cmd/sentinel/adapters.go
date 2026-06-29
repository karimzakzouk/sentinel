package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/v3/internal/agents"
	"github.com/karimzakzouk/sentinel/v3/internal/automerge"
	"github.com/karimzakzouk/sentinel/v3/internal/chatops"
	"github.com/karimzakzouk/sentinel/v3/internal/config"
	"github.com/karimzakzouk/sentinel/v3/internal/executors"
	"github.com/karimzakzouk/sentinel/v3/internal/feedback"
	githubapp "github.com/karimzakzouk/sentinel/v3/internal/github"
	"github.com/karimzakzouk/sentinel/v3/internal/k8s"
	"github.com/karimzakzouk/sentinel/v3/internal/llm"
	"github.com/karimzakzouk/sentinel/v3/internal/logging"
	"github.com/karimzakzouk/sentinel/v3/internal/prreview"
)

type storeAdapter struct {
	store *feedback.Store
}

func (a *storeAdapter) ListRecentIncidents(ctx context.Context, limit int) ([]any, error) {
	incidents, err := a.store.ListRecentIncidents(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(incidents))
	for i, inc := range incidents {
		out[i] = inc
	}
	return out, nil
}

func (a *storeAdapter) GetIncident(ctx context.Context, id uuid.UUID) (any, error) {
	inc, err := a.store.GetIncident(ctx, id)
	if err != nil {
		return nil, err
	}
	if inc == nil {
		return nil, nil
	}
	return inc, nil
}

func (a *storeAdapter) FindIncidentByPrefix(ctx context.Context, prefix string) (any, error) {
	inc, err := a.store.FindIncidentByPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if inc == nil {
		return nil, nil
	}
	return inc, nil
}

type vectorStoreAdapter struct {
	vs *feedback.VectorStore
}

func (a *vectorStoreAdapter) Search(ctx context.Context, text string, k int) ([]*agents.VectorMetadata, []float32, error) {
	metas, scores, err := a.vs.Search(ctx, text, k)
	if err != nil || len(metas) == 0 {
		return nil, nil, err
	}
	out := make([]*agents.VectorMetadata, len(metas))
	for i, m := range metas {
		out[i] = &agents.VectorMetadata{
			IncidentID: m.IncidentID,
			Service:    m.Service,
			RootCause:  m.RootCause,
			FixType:    m.FixType,
			FixPattern: m.FixPattern,
			Outcome:    m.Outcome,
		}
	}
	return out, scores, nil
}

func (a *vectorStoreAdapter) UsingHashFallback() bool {
	return a.vs.UsingHashFallback()
}

type prReviewVectorAdapter struct {
	vs *feedback.VectorStore
}

func (a *prReviewVectorAdapter) Search(ctx context.Context, text string, k int) ([]*prreview.VectorMetadata, []float32, error) {
	metas, scores, err := a.vs.Search(ctx, text, k)
	if err != nil || len(metas) == 0 {
		return nil, nil, err
	}
	out := make([]*prreview.VectorMetadata, len(metas))
	for i, m := range metas {
		out[i] = &prreview.VectorMetadata{
			IncidentID: m.IncidentID,
			Service:    m.Service,
			RootCause:  m.RootCause,
			FixType:    m.FixType,
			FixPattern: m.FixPattern,
			Outcome:    m.Outcome,
		}
	}
	return out, scores, nil
}

func (a *prReviewVectorAdapter) UsingHashFallback() bool {
	return a.vs.UsingHashFallback()
}

type chatopsStoreAdapter struct {
	store *feedback.Store
}

func (a *chatopsStoreAdapter) FindIncidentByPR(ctx context.Context, repo string, prNumber int) (*chatops.Incident, error) {
	inc, err := a.store.FindIncidentByPR(ctx, repo, prNumber)
	if err != nil || inc == nil {
		return nil, err
	}
	out := &chatops.Incident{
		ID:        inc.IncidentID.String(),
		Service:   inc.Cluster.Service,
		Namespace: inc.Cluster.Namespace,
		Status:    string(inc.Status),
	}
	if inc.State.RootCause != nil {
		out.RootCause = inc.State.RootCause.Statement
		out.Confidence = inc.State.RootCause.Confidence
		out.HasSchemaMigration = inc.State.RootCause.HasSchemaMigration
	}
	if inc.State.FixProposal != nil {
		out.FixType = string(inc.State.FixProposal.FixType)
		if inc.State.FixProposal.BaseBranch != nil {
			out.BaseBranch = *inc.State.FixProposal.BaseBranch
		}
		if inc.State.FixProposal.BranchName != nil {
			out.PRBranch = *inc.State.FixProposal.BranchName
		}
	}
	if inc.State.ExecutionResult != nil {
		if inc.State.ExecutionResult.PRURL != nil {
			out.PRURL = *inc.State.ExecutionResult.PRURL
		}
		if inc.State.ExecutionResult.PRNumber != nil {
			out.PRNumber = *inc.State.ExecutionResult.PRNumber
		}
		out.SandboxVerified = inc.State.ExecutionResult.SandboxVerified
		if inc.State.ExecutionResult.SandboxSummary != nil {
			out.SandboxSummary = *inc.State.ExecutionResult.SandboxSummary
		}
	}
	out.PRRepo = repo
	out.AgentTrace = inc.State.AgentTrace
	for _, h := range inc.State.Hypotheses {
		out.Hypotheses = append(out.Hypotheses, chatops.Hypothesis{
			ID:         h.HID,
			Statement:  h.Statement,
			Confidence: h.Confidence,
			Falsified:  h.Falsified,
		})
	}
	if inc.State.GovernanceDecision != nil {
		out.PolicyVerdict = string(inc.State.GovernanceDecision.Verdict)
	}
	return out, nil
}

func (a *chatopsStoreAdapter) LogCommandEvent(ctx context.Context, ev *chatops.CommandEvent) error {
	fbEv := &feedback.CommandEvent{
		IncidentID:       ev.IncidentID,
		PRNumber:         ev.PRNumber,
		PRRepo:           ev.PRRepo,
		Command:          ev.Command,
		Args:             ev.Args,
		Author:           ev.Author,
		AuthorPermission: ev.AuthorPermission,
		Result:           ev.Result,
		ResultDetail:     ev.ResultDetail,
		Timestamp:        ev.Timestamp,
	}
	return a.store.LogCommandEvent(ctx, fbEv)
}

type slogAdapter struct{}

func (s *slogAdapter) Info(msg string, args ...any)  { logging.Info(msg, args...) }
func (s *slogAdapter) Warn(msg string, args ...any)  { logging.Warn(msg, args...) }
func (s *slogAdapter) Error(msg string, args ...any) { logging.Error(msg, args...) }

type autoMergeStoreAdapter struct {
	store *feedback.Store
}

func (a *autoMergeStoreAdapter) UpdateAutoMergeStatus(ctx context.Context, incidentID string, status string) error {
	return a.store.UpdateAutoMergeStatus(ctx, uuid.MustParse(incidentID), status)
}

type autoMergeOutcomeAdapter struct {
	watcher *feedback.OutcomeWatcher
	policy  *automerge.Policy
}

func (a *autoMergeOutcomeAdapter) WatchPostMerge(ctx context.Context, incidentID, service, namespace string) {
	if a.watcher == nil {
		return
	}
	go func() {
		a.watcher.WatchWithDetails(ctx, service, namespace, incidentID, true, "code_fix", "")
	}()
}

type autoMergeClientAdapter struct {
	merger *automerge.Merger
	policy *automerge.Policy
}

func (a *autoMergeClientAdapter) Track(incidentID, repo string, prNumber int, baseBranch string, input chatops.AutoMergeInput) {
	a.merger.Track(&automerge.TrackedPR{
		IncidentID: incidentID,
		Repo:       repo,
		PRNumber:   prNumber,
		BaseBranch: baseBranch,
		Input: automerge.Input{
			IncidentID:          input.IncidentID,
			Service:             input.Service,
			Namespace:           input.Namespace,
			FixType:             input.FixType,
			HasSchemaMigration:  input.HasSchemaMigration,
			IsHighRisk:          input.IsHighRisk,
			Confidence:          input.Confidence,
			SandboxVerified:     input.SandboxVerified,
			PolicyVerdict:       input.PolicyVerdict,
			AutoMergeAnnotation: input.AutoMergeAnnotation,
		},
	})
}

func (a *autoMergeClientAdapter) Untrack(repo string, prNumber int) {
	a.merger.Untrack(repo, prNumber)
}

func (a *autoMergeClientAdapter) EvaluateNow(ctx context.Context, repo string, prNumber int) {
	a.merger.EvaluateNow(ctx, repo, prNumber)
}

func (a *autoMergeClientAdapter) Status() chatops.AutoMergeStatus {
	s := a.policy.Status()
	return chatops.AutoMergeStatus{
		Enabled:        s.Enabled,
		Namespaces:     s.Namespaces,
		MinConfidence:  s.MinConfidence,
		RequireCIGreen: s.RequireCIGreen,
		MaxPerHour:     s.MaxPerHour,
		MergesThisHour: s.MergesThisHour,
		CooldownActive: s.CooldownActive,
		CooldownReason: s.CooldownReason,
	}
}

func (a *autoMergeClientAdapter) RecordRegression(reason string) {
	a.policy.RecordRegression(reason)
}

func (a *autoMergeClientAdapter) ClearCooldown() {
}

type chatopsPipelineAdapter struct {
	pipeline *agents.Pipeline
	store    *feedback.Store
}

func (a *chatopsPipelineAdapter) RetryFixProposal(ctx context.Context, incidentID string) error {
	inc, err := a.store.GetIncident(ctx, uuid.MustParse(incidentID))
	if err != nil || inc == nil {
		return fmt.Errorf("incident not found: %w", err)
	}
	return a.pipeline.RetryFixProposal(ctx, &inc.State)
}

func (a *chatopsPipelineAdapter) Reinvestigate(ctx context.Context, incidentID string) error {
	inc, err := a.store.GetIncident(ctx, uuid.MustParse(incidentID))
	if err != nil || inc == nil {
		return fmt.Errorf("incident not found: %w", err)
	}
	inc.State.Hypotheses = nil
	inc.State.Evidence = nil
	inc.State.RootCause = nil
	inc.State.FixProposal = nil
	inc.State.Iterations = 0
	return a.pipeline.Run(ctx, &inc.State)
}

type chatopsSandboxAdapter struct {
	sandbox   *executors.SandboxRunner
	k8sClient *k8s.Client
	store     *feedback.Store
	ghExec    *executors.GitHubExecutor
	kaniko    *executors.KanikoBuilder
	cfg       *config.Config
}

func (a *chatopsSandboxAdapter) RunForIncident(ctx context.Context, incidentID string) (bool, string, error) {
	inc, err := a.store.GetIncident(ctx, uuid.MustParse(incidentID))
	if err != nil || inc == nil {
		return false, "incident not found", err
	}
	if inc.State.FixProposal == nil {
		return false, "no fix proposal on this incident", nil
	}
	result := a.sandbox.Run(ctx, inc)
	return result.Verified, result.Summary, nil
}

type providersStatusAdapter struct {
	router *llm.Router
}

func (a *providersStatusAdapter) ProviderStatuses() []any {
	pool := a.router.Pool()
	if pool == nil {
		return []any{}
	}
	statuses := pool.Status()
	out := make([]any, len(statuses))
	for i, s := range statuses {
		out[i] = s
	}
	return out
}

type autoMergeStatusAdapter struct {
	policy *automerge.Policy
	merger *automerge.Merger
}

func (a *autoMergeStatusAdapter) AutoMergeStatus() any {
	status := a.policy.Status()
	tracked := a.merger.TrackedPRs()
	trackedOut := make([]map[string]any, 0, len(tracked))
	for _, pr := range tracked {
		trackedOut = append(trackedOut, map[string]any{
			"incident_id":  pr.IncidentID,
			"repo":         pr.Repo,
			"pr_number":    pr.PRNumber,
			"base_branch":  pr.BaseBranch,
			"added_at":     pr.AddedAt,
			"last_eval":    pr.LastEval,
			"ci_state":     pr.Input.CIStatus.State,
			"confidence":   pr.Input.Confidence,
			"service":      pr.Input.Service,
			"namespace":    pr.Input.Namespace,
		})
	}
	return map[string]any{
		"enabled":          status.Enabled,
		"namespaces":       status.Namespaces,
		"min_confidence":   status.MinConfidence,
		"require_ci_green": status.RequireCIGreen,
		"max_per_hour":     status.MaxPerHour,
		"merges_this_hour": status.MergesThisHour,
		"cooldown_active":  status.CooldownActive,
		"cooldown_until":   status.CooldownUntil,
		"cooldown_reason":  status.CooldownReason,
		"tracked_prs":      trackedOut,
	}
}

type chatopsTokenProviderAdapter struct {
	app *githubapp.App
}

func (a *chatopsTokenProviderAdapter) GetInstallToken(ctx context.Context) (string, error) {
	return a.app.GetInstallToken(ctx)
}

type automergeTokenProviderAdapter struct {
	app *githubapp.App
}

func (a *automergeTokenProviderAdapter) GetInstallToken(ctx context.Context) (string, error) {
	return a.app.GetInstallToken(ctx)
}
