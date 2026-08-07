package automerge

import (
	"context"
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
	defer m.mu.Unlock()
	key := prKey(pr.Repo, pr.PRNumber)
	if _, exists := m.tracked[key]; exists {
		return
	}
	pr.AddedAt = time.Now()
	m.tracked[key] = pr
	logging.Info("automerge: tracking PR",
		"repo", pr.Repo, "pr", pr.PRNumber, "incident", pr.IncidentID)
}

// Untrack removes a PR from the watch list. Called when the PR is merged,
// closed without merge, /sentinel no-merge is invoked, or CI times out.
func (m *Merger) Untrack(repo string, prNumber int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tracked, prKey(repo, prNumber))
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
				fmt.Sprintf("⏱️ Auto-merge cancelled: CI did not complete within %v. Merging manually.", timeout))
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
