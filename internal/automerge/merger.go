package automerge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Merger is the auto-merge orchestrator. It maintains a list of tracked PRs
// (PRs that Sentinel opened and are pending auto-merge), polls their CI
// status, and merges them when all gates pass.
//
// Commands (/sentinel approve, /sentinel no-merge) mutate labels. The merger
// watches labels + gates + CI status. The merger is the ONLY actor that
// merges. This separation makes the system composable + auditable.
type Merger struct {
	policy  *Policy
	gh      GitHubClient
	store   Store
	outcome OutcomeWatcher
	mu      sync.Mutex
	tracked map[string]*TrackedPR
	stopCh  chan struct{}
	prLocks sync.Map

	// persist survives restarts. When nil, tracked PRs are memory-only
	// (pre-1.7 behavior, used by tests).
	persist TrackedPRStore

	onMerged  func(repo string, prNumber int)
	onSkipped func(repo string, prNumber int, reason string)
}

// TrackedPR is a PR pending auto-merge evaluation.
type TrackedPR struct {
	IncidentID  string
	Repo        string
	PRNumber    int
	Branch      string
	BaseBranch  string
	AddedAt     time.Time
	CIWaitStart time.Time
	LastEval    time.Time
	Input       Input
}

// GitHubClient is the GitHub API surface the merger needs.
type GitHubClient interface {
	GetCIStatus(ctx context.Context, repo string, prNumber int) (CIStatus, error)
	GetLabels(ctx context.Context, repo string, prNumber int) ([]string, error)
	Merge(ctx context.Context, repo string, prNumber int, commitMessage string) error
	PostComment(ctx context.Context, repo string, prNumber int, body string) error
	RevertMergeCommit(ctx context.Context, repo, mergeCommitSHA, baseBranch, reason string) (revertPRNumber int, err error)
	// IsPROpen reports whether the PR exists and is in the open state.
	// Used by restart reconciliation and by the poll loop to prune PRs
	// that were merged or closed by a human while Sentinel was down.
	IsPROpen(ctx context.Context, repo string, prNumber int) (bool, error)
}

// Store is the storage interface the merger needs.
type Store interface {
	UpdateAutoMergeStatus(ctx context.Context, incidentID string, status string) error
}

// OutcomeWatcher starts the post-merge regression watch for a merged PR.
type OutcomeWatcher interface {
	WatchPostMerge(ctx context.Context, incidentID, service, namespace string)
}

// NewMerger creates a Merger.
func NewMerger(policy *Policy, gh GitHubClient, store Store, outcome OutcomeWatcher) *Merger {
	return &Merger{
		policy:  policy,
		gh:      gh,
		store:   store,
		outcome: outcome,
		tracked: make(map[string]*TrackedPR),
		stopCh:  make(chan struct{}),
	}
}

// SetPersistence wires the tracked-PR store (feedback.Store). Call before
// Run/Reconcile. When set, Track/Untrack also write through to SQLite so a
// restart can re-adopt in-flight auto-merges via Reconcile.
func (m *Merger) SetPersistence(p TrackedPRStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persist = p
}

// SetOnMerged sets a callback invoked after a PR is successfully merged.
// Used by main.go to update the [SENTINELSTATUS] comment with the merge
// result.
func (m *Merger) SetOnMerged(fn func(repo string, prNumber int)) {
	m.onMerged = fn
}

// SetOnSkipped sets a callback invoked when a PR is skipped from auto-merge.
func (m *Merger) SetOnSkipped(fn func(repo string, prNumber int, reason string)) {
	m.onSkipped = fn
}

// persistSave writes a tracked PR to the persistence layer (best effort —
// a persistence failure logs but never blocks the in-memory operation).
func (m *Merger) persistSave(pr *TrackedPR) {
	if m.persist == nil {
		return
	}
	rec, err := pr.toRecord()
	if err != nil {
		logging.Error("automerge: failed to marshal tracked PR", "repo", pr.Repo, "pr", pr.PRNumber, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.persist.SaveTrackedPR(ctx, rec); err != nil {
		logging.Error("automerge: failed to persist tracked PR", "repo", pr.Repo, "pr", pr.PRNumber, "err", err)
	}
}

// persistDelete removes a tracked PR from the persistence layer.
func (m *Merger) persistDelete(repo string, prNumber int) {
	if m.persist == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.persist.DeleteTrackedPR(ctx, repo, prNumber); err != nil {
		logging.Error("automerge: failed to delete persisted tracked PR", "repo", repo, "pr", prNumber, "err", err)
	}
}

// prLock returns (or creates) a per-PR mutex. The same mutex is returned for
// every call with the same repo#prNumber so that EvaluateNow and the 30s
// poll loop cannot race on the same PR.
func (m *Merger) prLock(repo string, prNumber int) *sync.Mutex {
	key := fmt.Sprintf("%s#%d", repo, prNumber)
	val, _ := m.prLocks.LoadOrStore(key, &sync.Mutex{})
	mu, ok := val.(*sync.Mutex)
	if !ok {
		return &sync.Mutex{}
	}
	return mu
}

// Track adds a PR to the auto-merge watch list. Called after Sentinel opens
// a PR and the auto-merge policy says it's eligible (or pending CI).
func (m *Merger) Track(pr *TrackedPR) {
	m.mu.Lock()
	key := prKey(pr.Repo, pr.PRNumber)
	if _, exists := m.tracked[key]; exists {
		m.mu.Unlock()
		return
	}
	pr.AddedAt = time.Now()
	m.tracked[key] = pr
	m.mu.Unlock()

	m.persistSave(pr)

	logging.Info("automerge: tracking PR",
		"repo", pr.Repo, "pr", pr.PRNumber, "incident", pr.IncidentID)
}

// Untrack removes a PR from the watch list. Called when the PR is merged,
// closed without merge, /sentinel no-merge is invoked, or CI times out.
func (m *Merger) Untrack(repo string, prNumber int) {
	m.mu.Lock()
	delete(m.tracked, prKey(repo, prNumber))
	m.mu.Unlock()

	m.persistDelete(repo, prNumber)
}

// Reconcile re-adopts tracked PRs after a restart. Loads persisted records,
// drops those whose PR is no longer open (merged/closed while Sentinel was
// down), and re-tracks the rest. Call once at startup before Run.
func (m *Merger) Reconcile(ctx context.Context) {
	m.mu.Lock()
	persist := m.persist
	m.mu.Unlock()
	if persist == nil {
		logging.Info("automerge: no persistence configured — skipping restart reconciliation")
		return
	}

	records, err := persist.ListTrackedPRs(ctx)
	if err != nil {
		logging.Error("automerge: failed to load persisted tracked PRs", "err", err)
		return
	}
	if len(records) == 0 {
		logging.Info("automerge: no in-flight PRs to re-adopt")
		return
	}

	adopted, pruned := 0, 0
	for _, rec := range records {
		pr, err := rec.toTrackedPR()
		if err != nil {
			logging.Error("automerge: skipping corrupt tracked-PR record", "repo", rec.Repo, "pr", rec.PRNumber, "err", err)
			_ = persist.DeleteTrackedPR(ctx, rec.Repo, rec.PRNumber)
			continue
		}

		open, err := m.gh.IsPROpen(ctx, rec.Repo, rec.PRNumber)
		if err != nil {
			// GitHub API failure — keep the record, adopt optimistically.
			// The poll loop will prune it on the next tick if it's closed.
			logging.Warn("automerge: could not verify PR state during reconciliation — adopting anyway",
				"repo", rec.Repo, "pr", rec.PRNumber, "err", err)
			m.Track(pr)
			adopted++
			continue
		}
		if !open {
			// Merged or closed while Sentinel was down. We can't tell which
			// cheaply; record the neutral status and prune.
			logging.Info("automerge: PR no longer open — pruning tracked record",
				"repo", rec.Repo, "pr", rec.PRNumber)
			_ = m.store.UpdateAutoMergeStatus(ctx, rec.IncidentID, "skipped:closed_while_down")
			_ = persist.DeleteTrackedPR(ctx, rec.Repo, rec.PRNumber)
			pruned++
			continue
		}
		m.Track(pr)
		adopted++
	}
	logging.Info("automerge: restart reconciliation complete",
		"adopted", adopted, "pruned", pruned)
}

// TrackedPRs returns a snapshot of all tracked PRs (for the web UI).
func (m *Merger) TrackedPRs() []*TrackedPR {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*TrackedPR, 0, len(m.tracked))
	for _, pr := range m.tracked {
		out = append(out, pr)
	}
	return out
}

// IsTracked reports whether a PR is currently in the watch list.
func (m *Merger) IsTracked(repo string, prNumber int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.tracked[prKey(repo, prNumber)]
	return ok
}

// Run starts the merger loop. Polls tracked PRs every pollInterval (default
// 30s), evaluates gates, merges when all pass. Blocks until ctx is cancelled
// or Stop is called.
func (m *Merger) Run(ctx context.Context, pollInterval time.Duration) {
	if pollInterval <= 0 {
		pollInterval = 30 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	logging.Info("automerge: merger started", "poll_interval", pollInterval)

	for {
		select {
		case <-ctx.Done():
			logging.Info("automerge: merger stopping")
			return
		case <-m.stopCh:
			logging.Info("automerge: merger stopping (stop signal)")
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

// Stop signals the merger to stop. Safe to call multiple times.
func (m *Merger) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
}

// tick evaluates all tracked PRs and merges any that pass all gates.
func (m *Merger) tick(ctx context.Context) {
	m.mu.Lock()
	snapshot := make([]*TrackedPR, 0, len(m.tracked))
	for _, pr := range m.tracked {
		snapshot = append(snapshot, pr)
	}
	m.mu.Unlock()

	for _, pr := range snapshot {
		m.evaluateAndMerge(ctx, pr)
	}
}

// EvaluateNow forces an immediate evaluation of a specific PR. Called from
// the webhook handler when a check_run event arrives for a tracked PR.
func (m *Merger) EvaluateNow(ctx context.Context, repo string, prNumber int) {
	m.mu.Lock()
	pr := m.tracked[prKey(repo, prNumber)]
	m.mu.Unlock()
	if pr == nil {
		return
	}
	m.evaluateAndMerge(ctx, pr)
}

// evaluateAndMerge runs the policy and merges if approved. The per-PR mutex
// prevents the poll loop and EvaluateNow from racing on the same PR.
func (m *Merger) evaluateAndMerge(ctx context.Context, pr *TrackedPR) {
	lock := m.prLock(pr.Repo, pr.PRNumber)
	lock.Lock()
	defer lock.Unlock()

	// Re-check that the PR is still tracked — another caller may have
	// merged it while we waited for the lock.
	m.mu.Lock()
	_, stillTracked := m.tracked[prKey(pr.Repo, pr.PRNumber)]
	m.mu.Unlock()
	if !stillTracked {
		return
	}

	// If a human merged or closed the PR outside Sentinel, stop tracking it.
	// Covers the restart gap: a PR merged while Sentinel was down shows up
	// here on the first tick after reconciliation adopted it.
	open, err := m.gh.IsPROpen(ctx, pr.Repo, pr.PRNumber)
	if err == nil && !open {
		logging.Info("automerge: PR no longer open — untracking",
			"repo", pr.Repo, "pr", pr.PRNumber)
		_ = m.store.UpdateAutoMergeStatus(ctx, pr.IncidentID, "closed_before_merge")
		m.Untrack(pr.Repo, pr.PRNumber)
		return
	}
	// On error, fall through — GetCIStatus below will surface real problems.

	ciStatus, err := m.gh.GetCIStatus(ctx, pr.Repo, pr.PRNumber)
	if err != nil {
		logging.Error("automerge: failed to get CI status",
			"repo", pr.Repo, "pr", pr.PRNumber, "err", err)
		return
	}
	labels, err := m.gh.GetLabels(ctx, pr.Repo, pr.PRNumber)
	if err != nil {
		logging.Error("automerge: failed to get labels",
			"repo", pr.Repo, "pr", pr.PRNumber, "err", err)
		return
	}

	pr.Input.CIStatus = ciStatus
	pr.Input.HoldLabel = containsLabel(labels, "sentinel/hold")
	pr.Input.NoMergeLabel = containsLabel(labels, "sentinel/no-merge")
	pr.Input.ApprovedLabel = containsLabel(labels, "sentinel/approved")
	pr.LastEval = time.Now()

	if ciStatus.State == "pending" && !pr.CIWaitStart.IsZero() {
		timeout := time.Duration(m.policy.cfg.CITimeoutSec) * time.Second
		if time.Since(pr.CIWaitStart) > timeout {
			logging.Info("automerge: CI timeout — untracking",
				"repo", pr.Repo, "pr", pr.PRNumber)
			_ = m.gh.PostComment(ctx, pr.Repo, pr.PRNumber,
				fmt.Sprintf("⏱️ Auto-merge cancelled: CI did not complete within %v. Merge the PR manually instead.", timeout))
			_ = m.store.UpdateAutoMergeStatus(ctx, pr.IncidentID, "skipped:ci_timeout")
			m.Untrack(pr.Repo, pr.PRNumber)
			return
		}
	}
	if ciStatus.State == "pending" && pr.CIWaitStart.IsZero() {
		pr.CIWaitStart = time.Now()
	}

	decision := m.policy.Evaluate(pr.Input)
	if !decision.AutoMerge {
		logging.Info("automerge: not merging",
			"repo", pr.Repo, "pr", pr.PRNumber,
			"reason", decision.Reason,
			"gates_passed", decision.GatesPassed,
			"gates_failed", decision.GatesFailed)

		// Pending CI is not a failure — keep waiting.
		if ciStatus.State == "pending" {
			return
		}

		// Hard gate failure — untrack and post a comment so the reviewer
		// knows why.
		if len(decision.GatesFailed) > 0 {
			body := fmt.Sprintf("ℹ️ Auto-merge skipped: %s\n\nGates failed:\n%s",
				decision.Reason, formatGatesList(decision.GatesFailed, false))
			_ = m.gh.PostComment(ctx, pr.Repo, pr.PRNumber, body)
			_ = m.store.UpdateAutoMergeStatus(ctx, pr.IncidentID, "skipped:"+strings.Join(decision.GatesFailed, ","))
			m.Untrack(pr.Repo, pr.PRNumber)
		} else if len(decision.GatesPassed) == 0 {
			// Pre-gate failure (missing annotation, wrong namespace, etc.).
			body := fmt.Sprintf("ℹ️ Auto-merge skipped: %s", decision.Reason)
			_ = m.gh.PostComment(ctx, pr.Repo, pr.PRNumber, body)
			_ = m.store.UpdateAutoMergeStatus(ctx, pr.IncidentID, "skipped:"+decision.Reason)
			m.Untrack(pr.Repo, pr.PRNumber)

			if m.onSkipped != nil {
				m.onSkipped(pr.Repo, pr.PRNumber, decision.Reason)
			}
		}
		return
	}

	logging.Info("automerge: merging PR",
		"repo", pr.Repo, "pr", pr.PRNumber, "incident", pr.IncidentID)
	commitMsg := fmt.Sprintf("Auto-merged by Sentinel (incident %s, confidence %.2f, sandbox verified)",
		pr.IncidentID, pr.Input.Confidence)

	if err := m.gh.Merge(ctx, pr.Repo, pr.PRNumber, commitMsg); err != nil {
		logging.Error("automerge: merge failed",
			"repo", pr.Repo, "pr", pr.PRNumber, "err", err)
		_ = m.gh.PostComment(ctx, pr.Repo, pr.PRNumber,
			fmt.Sprintf("❌ Auto-merge failed: %v\n\nThe PR is still open — merge manually or run `/sentinel merge`.", err))
		_ = m.store.UpdateAutoMergeStatus(ctx, pr.IncidentID, "skipped:merge_failed")
		m.Untrack(pr.Repo, pr.PRNumber)
		return
	}

	m.policy.RecordMerge()
	_ = m.store.UpdateAutoMergeStatus(ctx, pr.IncidentID, "merged")
	m.Untrack(pr.Repo, pr.PRNumber)

	body := fmt.Sprintf("✅ Auto-merged into `%s`\n\nGates passed:\n%s\n\nMonitoring deployment...",
		pr.BaseBranch, formatGatesList(decision.GatesPassed, true))
	_ = m.gh.PostComment(ctx, pr.Repo, pr.PRNumber, body)

	if m.onMerged != nil {
		m.onMerged(pr.Repo, pr.PRNumber)
	}

	if m.outcome != nil {
		m.outcome.WatchPostMerge(ctx, pr.IncidentID, pr.Input.Service, pr.Input.Namespace)
	}

	logging.Info("automerge: merged PR", "repo", pr.Repo, "pr", pr.PRNumber)
}

// containsLabel reports whether labels contains target.
func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// prKey builds the map key for a tracked PR.
func prKey(repo string, prNumber int) string {
	return fmt.Sprintf("%s#%d", repo, prNumber)
}

// toRecord converts a TrackedPR to its persistence representation.
func (pr *TrackedPR) toRecord() (TrackedPRRecord, error) {
	inputJSON, err := json.Marshal(pr.Input)
	if err != nil {
		return TrackedPRRecord{}, fmt.Errorf("marshal input: %w", err)
	}
	return TrackedPRRecord{
		IncidentID: pr.IncidentID,
		Repo:       pr.Repo,
		PRNumber:   pr.PRNumber,
		Branch:     pr.Branch,
		BaseBranch: pr.BaseBranch,
		InputJSON:  inputJSON,
		AddedAt:    pr.AddedAt,
	}, nil
}

// toTrackedPR restores a TrackedPR from its persistence representation.
func (r TrackedPRRecord) toTrackedPR() (*TrackedPR, error) {
	var input Input
	if err := json.Unmarshal(r.InputJSON, &input); err != nil {
		return nil, fmt.Errorf("unmarshal input: %w", err)
	}
	return &TrackedPR{
		IncidentID: r.IncidentID,
		Repo:       r.Repo,
		PRNumber:   r.PRNumber,
		Branch:     r.Branch,
		BaseBranch: r.BaseBranch,
		AddedAt:    r.AddedAt,
		Input:      input,
	}, nil
}
