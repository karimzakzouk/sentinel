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

	onMerged      func(repo string, prNumber int)
	onSkipped     func(repo string, prNumber int, reason string)
	onCIFailure   func(ctx context.Context, pr *TrackedPR)
	onDeployWatch func(ctx context.Context, pr *TrackedPR)
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

	// CIRetries counts CI-failure feedback revisions performed on this PR.
	// CIRetryInFlight guards against the 30s poll loop re-triggering the
	// revision while one is already running (memory only, not persisted).
	CIRetries       int
	CIRetryInFlight bool

	// PreexistingNoted records that the "CI red is preexisting on base"
	// comment has been posted for this PR (memory only, not persisted).
	PreexistingNoted bool
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

// SetOnCIFailure wires the CI-feedback loop. When CI fails on a tracked PR
// and retries remain, the merger invokes fn asynchronously with the tracked
// PR. fn should fetch the failing test output, feed it to the fix proposer,
// push a corrected commit to the same branch, and re-verify in the sandbox.
// When fn finishes successfully it MUST call FinishCIRetry so the merger
// resets the CI-wait timer for the new commit; on failure it should call
// GiveUpCIRetry (or Untrack directly).
func (m *Merger) SetOnCIFailure(fn func(ctx context.Context, pr *TrackedPR)) {
	m.onCIFailure = fn
}

// SetOnDeployWatch wires the post-merge deployment health check. After a
// successful merge the merger invokes fn asynchronously with the merged
// PR's full context (repo, PR number, branches, service, namespace). fn
// should watch the deployed service's pods and auto-revert on regression.
func (m *Merger) SetOnDeployWatch(fn func(ctx context.Context, pr *TrackedPR)) {
	m.onDeployWatch = fn
}

// FinishCIRetry clears the in-flight guard and resets the CI-wait timer so
// the (freshly pushed) commit gets a full CI timeout window. Called by the
// onCIFailure callback after a successful revision.
func (m *Merger) FinishCIRetry(repo string, prNumber int, sandboxVerified bool, sandboxLevels string) {
	m.mu.Lock()
	pr := m.tracked[prKey(repo, prNumber)]
	if pr != nil {
		pr.CIRetryInFlight = false
		pr.CIWaitStart = time.Time{}
		pr.Input.SandboxVerified = sandboxVerified
		pr.Input.SandboxLevels = sandboxLevels
	}
	m.mu.Unlock()
	if pr != nil {
		m.persistSave(pr)
	}
}

// GiveUpCIRetry clears the in-flight guard and untracks the PR (CI-feedback
// revision failed — the PR stays open for human review).
func (m *Merger) GiveUpCIRetry(ctx context.Context, repo string, prNumber int, reason string) {
	m.mu.Lock()
	pr := m.tracked[prKey(repo, prNumber)]
	if pr != nil {
		pr.CIRetryInFlight = false
	}
	m.mu.Unlock()
	if pr != nil {
		_ = m.store.UpdateAutoMergeStatus(ctx, pr.IncidentID, "skipped:ci_retry_failed")
		m.Untrack(repo, prNumber)
	}
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

	// ciPending covers both an explicit "pending" state and a "neutral"
	// state with zero check runs. A PR opened seconds ago has no check
	// runs yet, which is indistinguishable from pending — hard-failing it
	// untracks the PR before CI even starts (observed in the kind eval:
	// PR evaluated 2s after opening, "CI status unknown", untracked, and
	// the green CI six minutes later had nobody watching). The wait is
	// bounded by CITimeoutSec below either way.
	ciPending := ciStatus.State == "pending" ||
		(ciStatus.State == "neutral" && ciStatus.Checks == 0)

	if ciPending && !pr.CIWaitStart.IsZero() {
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
	if ciPending && pr.CIWaitStart.IsZero() {
		pr.CIWaitStart = time.Now()
	}

	// CI-failure feedback loop: a RED CI run on a tracked PR means the
	// repository's own test suite rejected the fix. Instead of abandoning
	// the PR, feed the failing test output back to the fix proposer and
	// push a corrected commit to the same branch (CI re-runs on the new
	// commit automatically). Bounded by cfg.MaxCIRetries.
	//
	// EXCEPT when the pre-PR repo-checks gate already proved the failures
	// preexisting on the base branch (run #30: shipping-api's correct fix
	// burned a revision on a base-red mvn suite it could never fix). In
	// that case CI red is not the fix's fault: note it once on the PR and
	// fall through to the policy gate, which overrides the CI gate.
	if ciStatus.State == "failure" && pr.Input.CIKnownPreexisting && !pr.CIRetryInFlight {
		if !pr.PreexistingNoted {
			pr.PreexistingNoted = true
			logging.Info("automerge: CI red is preexisting on base — skipping revision loop",
				"repo", pr.Repo, "pr", pr.PRNumber,
				"failing_checks", ciStatus.Failing, "total_checks", ciStatus.Checks)
			_ = m.gh.PostComment(ctx, pr.Repo, pr.PRNumber,
				fmt.Sprintf("ℹ️ CI is red, but the repo-checks gate ran the same commands on the base branch and confirmed these failures predate this fix (new failures: 0). Proceeding — the red check is not attributable to this change."))
		}
	} else if ciStatus.State == "failure" && pr.CIRetryInFlight {
		// A revision is already running for this PR - do nothing.
		return
	}
	if ciStatus.State == "failure" && m.onCIFailure != nil &&
		pr.CIRetries < m.policy.cfg.MaxCIRetries && !pr.Input.CIKnownPreexisting {
		pr.CIRetries++
		pr.CIRetryInFlight = true
		m.persistSave(pr)
		logging.Info("automerge: CI failed - feeding failure back to fix proposer for revision",
			"repo", pr.Repo, "pr", pr.PRNumber,
			"revision", pr.CIRetries, "max", m.policy.cfg.MaxCIRetries,
			"failing_checks", ciStatus.Failing, "total_checks", ciStatus.Checks)
		_ = m.gh.PostComment(ctx, pr.Repo, pr.PRNumber,
			fmt.Sprintf("❌ CI failed (%d/%d checks). Sentinel is revising the fix with the failing test output - attempt %d/%d.",
				ciStatus.Failing, ciStatus.Checks, pr.CIRetries, m.policy.cfg.MaxCIRetries))
		prCopy := *pr
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logging.Error("automerge: CI-feedback revision panicked", "repo", prCopy.Repo, "pr", prCopy.PRNumber, "panic", r)
				}
			}()
			m.onCIFailure(ctx, &prCopy)
		}()
		return
	}

	decision := m.policy.Evaluate(pr.Input)
	if !decision.AutoMerge {
		logging.Info("automerge: not merging",
			"repo", pr.Repo, "pr", pr.PRNumber,
			"reason", decision.Reason,
			"gates_passed", decision.GatesPassed,
			"gates_failed", decision.GatesFailed)

		// Pending (or not-yet-started) CI is not a failure — keep waiting.
		if ciPending {
			return
		}

		// Transient wait-states (run #30 post-mortem: user-api PR #13 was
		// untracked 30s into a 10-minute regression cooldown — the PR said
		// "resumes at 09:13:07" but there was nothing left to resume).
		// Cooldown and rate-limit gates are pauses, not verdicts on the
		// fix: keep the PR tracked and re-evaluate after the window.
		if allTransientGates(decision.GatesFailed) {
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

	// Post-merge deployment health check (honest merge verification):
	// watch the deployed pods for restarts / OOMKills; auto-revert on
	// regression. Runs asynchronously - the tick loop must not block.
	if m.onDeployWatch != nil {
		prCopy := *pr
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logging.Error("automerge: deployment watch panicked", "repo", prCopy.Repo, "pr", prCopy.PRNumber, "panic", r)
				}
			}()
			m.onDeployWatch(ctx, &prCopy)
		}()
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

// transientGates are policy gates that represent WAIT STATES, not verdicts
// on the fix itself: the regression cooldown pauses all merging for a
// fixed window, and the rate limiter throttles merges per hour. A PR whose
// only failed gates are these must stay tracked and be re-evaluated once
// the window passes (run #30: a verified PR was abandoned mid-cooldown).
var transientGates = map[string]bool{
	"cooldown":   true,
	"rate_limit": true,
}

// allTransientGates reports whether every failed gate is a wait-state
// (and there is at least one). An empty slice means a pre-gate failure
// (missing annotation etc.), which is NOT transient.
func allTransientGates(failed []string) bool {
	if len(failed) == 0 {
		return false
	}
	for _, g := range failed {
		if !transientGates[g] {
			return false
		}
	}
	return true
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
		CIRetries:  pr.CIRetries,
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
		CIRetries:  r.CIRetries,
		Input:      input,
	}, nil
}
