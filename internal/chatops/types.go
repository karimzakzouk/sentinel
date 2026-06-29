// Package chatops implements GitHub-PR-comment-driven command execution.
//
// v4.0.0: Sentinel now responds to /sentinel <command> comments on its PRs.
// This is the chatops layer — every command is parsed, permission-checked,
// executed, and the result is fed back via emoji reactions + status comments.
//
// Design principles (informed by Prow + Mergify research):
//   - Always ACK with a 👍 reaction immediately when a command is received
//     (Prow's #1 complaint was silence).
//   - One negation convention: every state-changing command takes a `cancel`
//     argument. No /un-, no /remove-.
//   - Labels as the gate, a separate merger as the actor. Commands never
//     merge directly — they flip labels, the auto-merger watches labels.
//   - Auto-generated command registry. /sentinel help is generated from
//     code, never out of sync.
//   - In-place editable [SENTINELSTATUS] comment for multi-step workflows
//     (Prow's [APPROVALNOTIFIER] pattern).
//   - Permission errors must be actionable — say *why* and *what to do*.
//   - No fun commands. Sentinel is a prod tool.
package chatops

import (
        "context"
        "fmt"
        "strings"
        "time"
)

// Command is a parsed /sentinel command from a PR comment.
type Command struct {
        // Name is the command without the /sentinel prefix. E.g. "explain",
        // "retry-sandbox", "alternatives". Case-insensitive.
        Name string

        // Args is the raw argument string (everything after the command name).
        // E.g. for "/sentinel verify /pay /health" args is "/pay /health".
        Args string

        // ArgList is args split on whitespace. Convenience for commands that
        // take a list (verify, apply A|B|C, override <check>).
        ArgList []string

        // Cancel is true when the command was invoked with "cancel" as the first
        // arg. E.g. "/sentinel hold cancel" → Name="hold", Cancel=true.
        // This is Sentinel's one negation convention — every state-changing
        // command can be undone with `cancel`.
        Cancel bool

        // PRNumber is the GitHub PR number the comment was posted on.
        PRNumber int

        // PRRepo is the full repo path (e.g. "karimzakzouk/demo-app").
        PRRepo string

        // Author is the GitHub username who posted the comment.
        Author string

        // CommentID is the GitHub comment ID (used for reacting).
        CommentID int64

        // CommentBody is the full body of the comment (used for the in-place
        // status comment — we don't accidentally parse our own status comment).
        CommentBody string

        // Timestamp is when the webhook was received.
        Timestamp time.Time
}

// ParseCommand extracts a /sentinel command from a comment body.
// Returns nil if the comment doesn't contain a parseable command.
//
// Rules (fixes Prow's #8 complaint — fragile line-anchored regex):
//   - The command must be on its own line (or be the entire comment).
//   - A command mid-sentence ("see /sentinel explain for details") is
//     recognized but NOT executed — only suggested.
//   - Trailing punctuation is stripped ("/sentinel help." → "help").
//   - @mentions of the bot first are allowed ("@sentinel-bot help" → "help").
//   - Multiple commands per comment are allowed, returned in order.
//
// Sentinel's bot username is detected via the @mention pattern OR by the
// literal "/sentinel " prefix.
func ParseCommand(commentBody, author string, prNumber int, prRepo string, commentID int64) []*Command {
        if commentBody == "" {
                return nil
        }
        if strings.Contains(commentBody, "[SENTINELSTATUS]") {
                // Our own status comment — never parse.
                return nil
        }

        var commands []*Command
        lines := strings.Split(commentBody, "\n")
        now := time.Now().UTC()

        for _, line := range lines {
                trimmed := strings.TrimSpace(line)
                if trimmed == "" {
                        continue
                }

                // Allow @sentinel-bot or @sentinel mentions as an alternative prefix.
                // Also accept the bare "/sentinel " prefix.
                lower := strings.ToLower(trimmed)
                var rest string
                switch {
                case strings.HasPrefix(lower, "/sentinel "):
                        rest = trimmed[len("/sentinel "):]
                case strings.HasPrefix(lower, "/sentinel"):
                        // "/sentinel" alone (no args) — only valid for /sentinel help
                        rest = trimmed[len("/sentinel"):]
                case strings.HasPrefix(lower, "@sentinel-bot "):
                        rest = trimmed[len("@sentinel-bot "):]
                case strings.HasPrefix(lower, "@sentinel "):
                        rest = trimmed[len("@sentinel "):]
                default:
                        continue
                }

                rest = strings.TrimSpace(rest)
                if rest == "" {
                        // "/sentinel" with nothing after — treat as "help"
                        commands = append(commands, &Command{
                                Name:       "help",
                                PRNumber:   prNumber,
                                PRRepo:     prRepo,
                                Author:     author,
                                CommentID:  commentID,
                                CommentBody: commentBody,
                                Timestamp:  now,
                        })
                        continue
                }

                // Strip trailing punctuation (. ! ? , ;)
                rest = strings.TrimRight(rest, ".!?,;")

                // Split into name + args on first whitespace
                parts := strings.SplitN(rest, " ", 2)
                name := strings.ToLower(parts[0])
                args := ""
                if len(parts) > 1 {
                        args = strings.TrimSpace(parts[1])
                }

                // Check for "cancel" negation
                cancel := false
                if args == "cancel" || strings.HasPrefix(args+" ", "cancel ") {
                        cancel = true
                        // Strip "cancel" prefix from args
                        args = strings.TrimSpace(strings.TrimPrefix(args, "cancel"))
                }

                argList := []string{}
                if args != "" {
                        argList = append(argList, strings.Fields(args)...)
                }

                commands = append(commands, &Command{
                        Name:        name,
                        Args:        args,
                        ArgList:     argList,
                        Cancel:      cancel,
                        PRNumber:    prNumber,
                        PRRepo:      prRepo,
                        Author:      author,
                        CommentID:   commentID,
                        CommentBody: commentBody,
                        Timestamp:   now,
                })
        }

        return commands
}

// Permission is the access tier required to run a command.
type Permission int

const (
        // PermRead — any collaborator with read access.
        PermRead Permission = iota
        // PermWrite — collaborator with write access (Tier 1+2 commands).
        PermWrite
        // PermAdmin — repo admin OR sentinel.io/admin-team member (Tier 3 commands).
        PermAdmin
)

func (p Permission) String() string {
        switch p {
        case PermRead:
                return "read"
        case PermWrite:
                return "write"
        case PermAdmin:
                return "admin"
        }
        return "unknown"
}

// CommandSpec describes a command for the registry + help generator.
type CommandSpec struct {
        // Name is the command name without /sentinel prefix. E.g. "retry-sandbox".
        Name string

        // Description is a one-line summary for /sentinel help.
        Description string

        // Usage shows the full invocation. E.g. "/sentinel verify <paths>".
        Usage string

        // Example shows a concrete example. E.g. "/sentinel verify /pay /health".
        Example string

        // Permission is the required access tier.
        Permission Permission

        // Tier is "Informational", "Operational", or "Destructive" — used to
        // group commands in /sentinel help output.
        Tier string

        // Cancelable is true if the command supports `cancel` negation.
        Cancelable bool

        // Handler is the function that executes the command.
        Handler CommandHandler
}

// CommandHandler executes a parsed command.
// Implementations should:
//   - Post a result comment on the PR (via the GitHubClient)
//   - Update the [SENTINELSTATUS] comment if state changed
//   - Return an error only for infrastructure failures (GitHub API down)
//     — permission failures and "PR not tracked" are NOT errors, they're
//     posted as comments and return nil.
type CommandHandler func(ctx context.Context, cmd *Command, deps *Deps) error

// Deps is the dependency bag passed to every command handler.
// Constructed once in main.go, passed to every Dispatch call.
type Deps struct {
        Store         Store
        GitHub        GitHubClient
        Router        LLMRouter
        Pipeline      PipelineRunner
        Sandbox       SandboxRunner
        Reactions     ReactionsClient
        StatusComment StatusCommentManager
        Logger        Logger

        // v4.0.0: Auto-merge client. When set, /sentinel approve, /sentinel hold,
        // /sentinel no-merge, /sentinel merge can interact with the auto-merger.
        // nil = auto-merge not configured (commands post "not configured" message).
        AutoMerge AutoMergeClient

        // v4.0.0: Pause controller (the slackbot implements this). When set,
        // /sentinel pause + /sentinel resume can pause autonomous mode.
        PauseController PauseController

        // v4.0.0: K8s rollback executor. When set, /sentinel rollback can
        // trigger kubectl rollout undo on a deployment. nil = not configured.
        Rollback RollbackExecutor
}

// AutoMergeClient is the auto-merger interface for chatops commands.
type AutoMergeClient interface {
        // Track adds a PR to the auto-merge watch list.
        Track(incidentID, repo string, prNumber int, baseBranch string, input AutoMergeInput)

        // Untrack removes a PR from the watch list.
        Untrack(repo string, prNumber int)

        // EvaluateNow forces an immediate evaluation of a tracked PR.
        EvaluateNow(ctx context.Context, repo string, prNumber int)

        // Status returns the current policy + rate-limiter + cooldown state.
        Status() AutoMergeStatus

        // RecordRegression triggers the cooldown (called by outcome watcher).
        RecordRegression(reason string)

        // ClearCooldown manually clears the cooldown (called by /sentinel override cooldown).
        ClearCooldown()
}

// AutoMergeInput is the input for the auto-merge policy.
type AutoMergeInput struct {
        IncidentID         string
        Service            string
        Namespace          string
        FixType            string
        HasSchemaMigration bool
        IsHighRisk         bool
        Confidence         float64
        SandboxVerified    bool
        PolicyVerdict      string
}

// AutoMergeStatus is the public state of the auto-merge policy.
type AutoMergeStatus struct {
        Enabled        bool   `json:"enabled"`
        Namespaces     []string `json:"namespaces"`
        MinConfidence  float64 `json:"min_confidence"`
        RequireCIGreen bool   `json:"require_ci_green"`
        MaxPerHour     int    `json:"max_per_hour"`
        MergesThisHour int    `json:"merges_this_hour"`
        CooldownActive bool   `json:"cooldown_active"`
        CooldownReason string `json:"cooldown_reason,omitempty"`
}

// PauseController is implemented by the slackbot. v4.0.0.
type PauseController interface {
        IsPaused() bool
        Pause(duration time.Duration, reason string)
        Resume()
}

// RollbackExecutor is implemented by executors.K8sExecutor. v4.0.0.
type RollbackExecutor interface {
        RollbackByName(ctx context.Context, namespace, deploymentName string) error
}

// Store is the storage interface chatops needs (subset of feedback.Store).
type Store interface {
        FindIncidentByPR(ctx context.Context, repo string, prNumber int) (*Incident, error)
        LogCommandEvent(ctx context.Context, ev *CommandEvent) error
}

// Incident is a minimal incident view for command handlers.
// (We use a separate type so chatops doesn't import schemas — keeps the
// package decoupled. The adapter in main.go converts.)
type Incident struct {
        ID         string
        Service    string
        Namespace  string
        Status     string
        RootCause  string
        Confidence float64
        PRURL      string
        PRNumber   int
        PRRepo     string
        PRBranch   string

        // v4.0.0: Extended fields for chatops commands.
        FixType            string
        HasSchemaMigration bool
        IsHighRisk         bool
        SandboxVerified    bool
        SandboxSummary     string
        PolicyVerdict      string // safety.Policy.Evaluate verdict
        BaseBranch         string

        // v4.2.5: Auto-merge status for the status comment.
        AutoMergeEnabled bool
        AutoMergeMerged  bool
        AutoMergeSkipped string // non-empty = skipped with reason

        // v4.0.0: Agent trace for /sentinel trace. Each entry is one agent step.
        AgentTrace []map[string]any

        // v4.0.0: Hypotheses for /sentinel trace + /sentinel explain.
        Hypotheses []Hypothesis
}

// Hypothesis is one candidate root cause from the investigator agent.
type Hypothesis struct {
        ID         string  `json:"h_id"`
        Statement  string  `json:"statement"`
        Confidence float64 `json:"confidence"`
        Falsified  bool    `json:"falsified"`
}

// CommandEvent is the audit-log entry for a /sentinel command.
// Matches feedback.CommandEvent.
type CommandEvent struct {
        IncidentID       string
        PRNumber         int
        PRRepo           string
        Command          string
        Args             string
        Author           string
        AuthorPermission string
        Result           string
        ResultDetail     string
        Timestamp        time.Time
}

// GitHubClient is the GitHub API surface chatops needs.
type GitHubClient interface {
        // ReactComment adds an emoji reaction to a PR comment.
        // Reactions: "+1", "-1", "laugh", "confused", "heart", "hooray", "rocket", "eyes", "thumbsup", "thumbsdown", "white_check_mark", "x".
        ReactComment(ctx context.Context, repo string, commentID int64, reaction string) error

        // RemoveReaction removes a reaction (for swapping 👍 → ✅ → ❌).
        RemoveReaction(ctx context.Context, repo string, commentID int64, reaction string) error

        // PostComment posts a new comment on a PR.
        PostComment(ctx context.Context, repo string, prNumber int, body string) error

        // FindStatusComment returns the ID + body of the [SENTINELSTATUS]
        // comment on this PR, or 0 if not found.
        FindStatusComment(ctx context.Context, repo string, prNumber int) (commentID int64, body string, err error)

        // UpdateComment edits an existing comment in place.
        UpdateComment(ctx context.Context, repo string, commentID int64, body string) error

        // GetPermission returns the user's permission level on the repo:
        // "read", "write", "admin", or "none".
        GetPermission(ctx context.Context, repo, username string) (string, error)

        // PostCommentWithID posts a comment and returns its ID (for follow-up reactions).
        PostCommentWithID(ctx context.Context, repo string, prNumber int, body string) (int64, error)

        // v4.0.0: Label management — used by /sentinel approve, /sentinel hold,
        // /sentinel no-merge. Labels are the gate; the merger watches them.
        SetLabels(ctx context.Context, repo string, prNumber int, labels []string) error
        RemoveLabel(ctx context.Context, repo string, prNumber int, label string) error
        GetLabels(ctx context.Context, repo string, prNumber int) ([]string, error)

        // v4.0.0: Merge — used by /sentinel merge. The merger is the only thing
        // that auto-merges, but /sentinel merge is the manual override that
        // merges through Sentinel (so the outcome watcher attaches).
        Merge(ctx context.Context, repo string, prNumber int, commitMessage string) error

        // v4.0.0: FindAlternativesComment returns the body of the [SENTINELALTERNATIVES]
        // comment (if any) — used by /sentinel apply to read the chosen approach.
        FindCommentByMarker(ctx context.Context, repo string, prNumber int, marker string) (string, error)
}

// LLMRouter is the LLM interface chatops needs (for /sentinel explain,
// /sentinel alternatives). Subset of llm.Router.
type LLMRouter interface {
        FrontierText(ctx context.Context, system, user string, maxTokens int) (string, error)
        FrontierJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error)
}

// PipelineRunner lets /sentinel retest + /sentinel investigate re-run the agent pipeline.
type PipelineRunner interface {
        // RetryFixProposal re-runs only the fix proposer with current state.
        RetryFixProposal(ctx context.Context, incidentID string) error

        // Reinvestigate re-runs the full pipeline from scratch.
        Reinvestigate(ctx context.Context, incidentID string) error
}

// SandboxRunner lets /sentinel retry-sandbox re-run sandbox verification.
type SandboxRunner interface {
        RunForIncident(ctx context.Context, incidentID string) (verified bool, summary string, err error)
}

// ReactionsClient manages emoji reactions on PR comments.
// Wraps the GitHubClient with stateful reaction swaps (👍 → ✅ on success).
type ReactionsClient interface {
        // Ack reacts to the command comment with 👍 (immediate acknowledgment).
        Ack(ctx context.Context, repo string, commentID int64) error

        // Success replaces the 👍 reaction with ✅.
        Success(ctx context.Context, repo string, commentID int64) error

        // Failure replaces the 👍 reaction with ❌.
        Failure(ctx context.Context, repo string, commentID int64) error

        // Pending replaces the 👍 reaction with 👀 (in-progress).
        Pending(ctx context.Context, repo string, commentID int64) error
}

// StatusCommentManager owns the in-place editable [SENTINELSTATUS] comment
// on each tracked PR. Prow's [APPROVALNOTIFIER] pattern — one comment per PR
// that's edited in place as state changes, instead of posting new comments.
type StatusCommentManager interface {
        // Update posts (or edits) the [SENTINELSTATUS] comment on the PR.
        // If no status comment exists yet, posts a new one. Otherwise edits
        // the existing one in place.
        Update(ctx context.Context, repo string, prNumber int, incident *Incident, extra string) error

        // Render generates the markdown body for the status comment.
        Render(incident *Incident, extra string) string
}

// Logger is a minimal logging interface (slog-compatible via the adapter).
type Logger interface {
        Info(msg string, args ...any)
        Warn(msg string, args ...any)
        Error(msg string, args ...any)
}

// ErrPermissionDenied is returned by Dispatch when the author lacks permission.
// The dispatcher posts a comment with the reason — this error is for logging.
type ErrPermissionDenied struct {
        Author     string
        Required   Permission
        HasPermission string
}

func (e *ErrPermissionDenied) Error() string {
        return fmt.Sprintf("%s lacks %s permission (has %s)", e.Author, e.Required, e.HasPermission)
}

// ErrNotTracked is returned when the PR doesn't have an associated Sentinel incident.
type ErrNotTracked struct {
        PRNumber int
        PRRepo   string
}

func (e *ErrNotTracked) Error() string {
        return fmt.Sprintf("PR %s#%d is not tracked by Sentinel", e.PRRepo, e.PRNumber)
}
