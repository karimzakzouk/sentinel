// Package automerge — Merger. Watches tracked PRs, evaluates gates, merges
// when all pass, starts the outcome watcher post-merge.
package automerge

import (
	"github.com/karimzakzouk/sentinel/internal/logging"
        "context"
        "fmt"
        "log"
        "strings"
        "sync"
        "time"
)

// Merger is the auto-merge orchestrator. It maintains a list of tracked PRs
// (PRs that Sentinel opened + are pending auto-merge), polls their CI status,
// and merges them when all gates pass.
//   - Commands (/sentinel approve, /sentinel no-merge) mutate labels.
//   - The merger watches labels + gates + CI status.
//   - The merger is the ONLY thing that actually merges.
//   - This separation makes the system composable + auditable.

type Merger struct {
        policy   *Policy
        gh       GitHubClient
        store    Store
        outcome  OutcomeWatcher
        mu       sync.Mutex
        tracked  map[string]*TrackedPR // key: "repo#pr_number"
        stopCh   chan struct{}

        prLocks   sync.Map // key: "repo#pr_number" → *sync.Mutex

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
        CIWaitStart time.Time // when we first saw CI pending
        LastEval    time.Time
        // Cached input fields (re-evaluated each tick in case labels changed)

        Input       Input
}

// GitHubClient is the GitHub API surface the merger needs.

type GitHubClient interface {
        GetCIStatus(ctx context.Context, repo string, prNumber int) (CIStatus, error)
        GetLabels(ctx context.Context, repo string, prNumber int) ([]string, error)
        Merge(ctx context.Context, repo string, prNumber int, commitMessage string) error
        PostComment(ctx context.Context, repo string, prNumber int, body string) error
}

// Store is the storage interface the merger needs.

type Store interface {
        UpdateAutoMergeStatus(ctx context.Context, incidentID string, status string) error
}

// OutcomeWatcher is the post-merge monitoring interface.

type OutcomeWatcher interface {
        // WatchPostMerge starts the Phase 1 (10 min) + Phase 2 (24h) outcome
        // watch for the merged PR. If a regression is detected, calls
        // policy.RecordRegression to activate the cooldown.

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

// SetOnMerged sets a callback invoked after a PR is successfully merged. v4.2.5.
// Used by main.go to update the [SENTINELSTATUS] comment with the merge result.

func (m *Merger) SetOnMerged(fn func(repo string, prNumber int)) {
        m.onMerged = fn
}

// SetOnSkipped sets a callback invoked when a PR is skipped from auto-merge. v4.2.5.

func (m *Merger) SetOnSkipped(fn func(repo string, prNumber int, reason string)) {
        m.onSkipped = fn
}

// prLock returns (or creates) a per-PR mutex.prevents EvaluateNow
// and the 30s poll from racing on the same PR (which caused duplicate merges
// + duplicate comments).

func (m *Merger) prLock(repo string, prNumber int) *sync.Mutex {
        key := fmt.Sprintf("%s#%d", repo, prNumber)
        val, loaded := m.prLocks.LoadOrStore(key, &sync.Mutex{})
        _ = loaded // we don't care if it was already there — we just need the mutex
        mu, ok := val.(*sync.Mutex)
        if !ok {
                return &sync.Mutex{} // fallback — should never happen
        }
        return mu
}

// Track adds a PR to the auto-merge watch list. Called after Sentinel opens
// a PR + the auto-merge policy says it's eligible (or pending CI).

func (m *Merger) Track(pr *TrackedPR) {
        m.mu.Lock()
        defer m.mu.Unlock()
        key := prKey(pr.Repo, pr.PRNumber)
        if _, exists := m.tracked[key]; exists {
                return // already tracked
        }
        pr.AddedAt = time.Now()
        m.tracked[key] = pr
        logging.Info("tracking PR # (incident )", "value", pr.Repo, pr.PRNumber, pr.IncidentID)
}

// Untrack removes a PR from the watch list. Called when:
//   - PR is merged (success)
//   - PR is closed without merge
//   - /sentinel no-merge is invoked
//   - CI timeout expires

func (m *Merger) Untrack(repo string, prNumber int) {
        m.mu.Lock()
        defer m.mu.Unlock()
        key := prKey(repo, prNumber)
        delete(m.tracked, key)
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

// Run starts the merger loop. Polls tracked PRs every pollInterval, evaluates
// gates, merges when all pass. Blocks until ctx is cancelled.
// pollInterval default: 30s. CI status changes are also detected via webhook
// (in prreview/reviewer.go) which can call EvaluateNow for immediate action.

func (m *Merger) Run(ctx context.Context, pollInterval time.Duration) {
        if pollInterval <= 0 {
                pollInterval = 30 * time.Second
        }
        ticker := time.NewTicker(pollInterval)
        defer ticker.Stop()

        logging.Info("merger started (poll interval: )", "pollinterval", pollInterval)

        for {
                select {
                case <-ctx.Done():
                        logging.Info("merger stopping")
                        return
                case <-m.stopCh:
                        logging.Info("merger stopping (stop signal)")
                        return
                case <-ticker.C:
                        m.tick(ctx)
                }
        }
}

// Stop signals the merger to stop.

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

// EvaluateNow forces an immediate evaluation of a specific PR (called from
// the webhook handler when a check_run event arrives for a tracked PR).

func (m *Merger) EvaluateNow(ctx context.Context, repo string, prNumber int) {
        m.mu.Lock()
        pr := m.tracked[prKey(repo, prNumber)]
        m.mu.Unlock()
        if pr == nil {
                return
        }
        m.evaluateAndMerge(ctx, pr)
}

// evaluateAndMerge runs the policy + merges if approved.
// racing on the same PR (which caused duplicate merges + duplicate comments).

func (m *Merger) evaluateAndMerge(ctx context.Context, pr *TrackedPR) {
        lock := m.prLock(pr.Repo, pr.PRNumber)
        lock.Lock()
        defer lock.Unlock()

        // by a previous evaluateAndMerge call that won the lock).

        m.mu.Lock()
        _, stillTracked := m.tracked[prKey(pr.Repo, pr.PRNumber)]
        m.mu.Unlock()
        if !stillTracked {
                return // already merged or untracked
        }
        // Fetch fresh CI status + labels.

        ciStatus, err := m.gh.GetCIStatus(ctx, pr.Repo, pr.PRNumber)
        if err != nil {
                logging.Error("failed to get CI status for #", "value", pr.Repo, pr.PRNumber, err)
                return
        }
        labels, err := m.gh.GetLabels(ctx, pr.Repo, pr.PRNumber)
        if err != nil {
                logging.Error("failed to get labels for #", "value", pr.Repo, pr.PRNumber, err)
                return
        }

        // Update the cached input with fresh data.

        pr.Input.CIStatus = ciStatus
        pr.Input.HoldLabel = containsLabel(labels, "sentinel/hold")
        pr.Input.NoMergeLabel = containsLabel(labels, "sentinel/no-merge")
        pr.Input.ApprovedLabel = containsLabel(labels, "sentinel/approved")
        pr.LastEval = time.Now()

        // Check CI timeout.

        if ciStatus.State == "pending" && !pr.CIWaitStart.IsZero() {
                timeout := time.Duration(m.policy.cfg.CITimeoutSec) * time.Second
                if time.Since(pr.CIWaitStart) > timeout {
                        logging.Info("CI timeout for # — untracking", "value", pr.Repo, pr.PRNumber)
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

        // Evaluate the policy.

        decision := m.policy.Evaluate(pr.Input)
        if !decision.AutoMerge {

                // (which happens when a pre-gate check fails, e.g. missing annotation).

                log.Printf("[automerge] PR %s#%d NOT auto-merging: %s (gates_passed=%v gates_failed=%v)",
                        pr.Repo, pr.PRNumber, decision.Reason, decision.GatesPassed, decision.GatesFailed)
                // If CI is pending, just wait — don't untrack.

                if ciStatus.State == "pending" {
                        return
                }
                // If gates failed (not pending), untrack + comment.

                if len(decision.GatesFailed) > 0 {
                        body := fmt.Sprintf("ℹ️ Auto-merge skipped: %s\n\nGates failed:\n%s",
                                decision.Reason, formatGatesList(decision.GatesFailed, false))
                        _ = m.gh.PostComment(ctx, pr.Repo, pr.PRNumber, body)
                        _ = m.store.UpdateAutoMergeStatus(ctx, pr.IncidentID, "skipped:"+strings.Join(decision.GatesFailed, ","))
                        m.Untrack(pr.Repo, pr.PRNumber)
                } else if len(decision.GatesPassed) == 0 {
                        // Pre-gate failure (e.g. missing annotation, disabled, wrong namespace) — untrack.

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

        // All gates passed — merge.

        logging.Info("merging PR # (incident )", "value", pr.Repo, pr.PRNumber, pr.IncidentID)
        commitMsg := fmt.Sprintf("Auto-merged by Sentinel (incident %s, confidence %.2f, sandbox verified)",
                pr.IncidentID, pr.Input.Confidence)

        if err := m.gh.Merge(ctx, pr.Repo, pr.PRNumber, commitMsg); err != nil {
                logging.Error("merge failed for #", "value", pr.Repo, pr.PRNumber, err)
                _ = m.gh.PostComment(ctx, pr.Repo, pr.PRNumber,
                        fmt.Sprintf("❌ Auto-merge failed: %v\n\nThe PR is still open — merge manually or run `/sentinel merge`.", err))
                _ = m.store.UpdateAutoMergeStatus(ctx, pr.IncidentID, "skipped:merge_failed")
                m.Untrack(pr.Repo, pr.PRNumber)
                return
        }

        // Success!

        m.policy.RecordMerge()
        _ = m.store.UpdateAutoMergeStatus(ctx, pr.IncidentID, "merged")
        m.Untrack(pr.Repo, pr.PRNumber)

        body := fmt.Sprintf("✅ Auto-merged into `%s`\n\nGates passed:\n%s\n\nMonitoring deployment...",
                pr.BaseBranch, formatGatesList(decision.GatesPassed, true))
        _ = m.gh.PostComment(ctx, pr.Repo, pr.PRNumber, body)

        if m.onMerged != nil {
                m.onMerged(pr.Repo, pr.PRNumber)
        }

        // Start post-merge outcome monitoring.

        if m.outcome != nil {
                m.outcome.WatchPostMerge(ctx, pr.IncidentID, pr.Input.Service, pr.Input.Namespace)
        }

        logging.Info("merged PR #", "value", pr.Repo, pr.PRNumber)
}

// containsLabel returns true if the labels slice contains the given label.

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
