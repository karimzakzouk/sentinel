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

	// Cancel is true when the command was invoked with "cancel" as the
	// first arg. E.g. "/sentinel hold cancel" → Name="hold", Cancel=true.
	Cancel bool

	// PRNumber is the GitHub PR number the comment was posted on.
	PRNumber int

	// PRRepo is the full repo path (e.g. "karimzakzouk/demo-app").
	PRRepo string

	// Author is the GitHub username who posted the comment.
	Author string

	// CommentID is the GitHub comment ID (used for reacting).
	CommentID int64

	// CommentBody is the full body of the comment — used to detect and
	// skip our own [SENTINELSTATUS] comment so we don't self-trigger.
	CommentBody string

	// Timestamp is when the webhook was received.
	Timestamp time.Time
}

// ParseCommand extracts /sentinel commands from a comment body. Returns nil
// if the comment is the bot's own [SENTINELSTATUS] comment or contains no
// parseable command. Multiple commands per comment are allowed, returned in
// order.
//
// Rules:
//
//   - The command must be on its own line (or be the entire comment).
//   - Trailing punctuation is stripped ("/sentinel help." → "help").
//   - @mentions of the bot are accepted as an alternative prefix:
//     "@sentinel-bot help" and "@sentinel help" both work.
//   - "/sentinel" with no arguments is treated as "/sentinel help".
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

		lower := strings.ToLower(trimmed)
		var rest string
		switch {
		case strings.HasPrefix(lower, "/sentinel "):
			rest = trimmed[len("/sentinel "):]
		case strings.HasPrefix(lower, "/sentinel"):
			// "/sentinel" alone (no args) — only valid for /sentinel help.
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
			// "/sentinel" with nothing after — treat as "help".
			commands = append(commands, &Command{
				Name:        "help",
				PRNumber:    prNumber,
				PRRepo:      prRepo,
				Author:      author,
				CommentID:   commentID,
				CommentBody: commentBody,
				Timestamp:   now,
			})
			continue
		}

		// Strip trailing punctuation (. ! ? , ;).
		rest = strings.TrimRight(rest, ".!?,;")

		parts := strings.SplitN(rest, " ", 2)
		name := strings.ToLower(parts[0])
		args := ""
		if len(parts) > 1 {
			args = strings.TrimSpace(parts[1])
		}

		cancel := false
		if args == "cancel" || strings.HasPrefix(args+" ", "cancel ") {
			cancel = true
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
	// PermAdmin — repo admin (Tier 3 commands).
	PermAdmin
)

// String returns the lowercase name of the permission tier.
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

// CommandHandler executes a parsed command. Implementations should post a
// result comment on the PR and update the [SENTINELSTATUS] comment if state
// changed. Return an error only for infrastructure failures (GitHub API
// down) — permission failures and "PR not tracked" are NOT errors: they're
// posted as comments and return nil.
type CommandHandler func(ctx context.Context, cmd *Command, deps *Deps) error

// Deps is the dependency bag passed to every command handler. Constructed
// once in cmd/sentinel and passed to every Dispatch call.
type Deps struct {
	Store         Store
	GitHub        GitHubClient
	Router        LLMRouter
	Pipeline      PipelineRunner
	Sandbox       SandboxRunner
	Reactions     ReactionsClient
	StatusComment StatusCommentManager

	// AutoMerge is the merger interface for /sentinel no-merge, /sentinel
	// merge. nil = auto-merge not configured (commands post a "not
	// configured" message).
	AutoMerge AutoMergeClient

	// PauseController is the slackbot. nil = pause/resume not configured.
	PauseController PauseController

	// Rollback is the kubectl rollout undo executor. nil = not configured.
	Rollback RollbackExecutor
}

// AutoMergeClient is the auto-merger interface for chatops commands.
type AutoMergeClient interface {
	Track(incidentID, repo string, prNumber int, baseBranch string, input AutoMergeInput)
	Untrack(repo string, prNumber int)
	EvaluateNow(ctx context.Context, repo string, prNumber int)
	Status() AutoMergeStatus
	RecordRegression(reason string)
	ClearCooldown()
}

// AutoMergeInput is the input for the auto-merge policy.
type AutoMergeInput struct {
	IncidentID          string
	Service             string
	Namespace           string
	FixType             string
	HasSchemaMigration  bool
	IsHighRisk          bool
	Confidence          float64
	SandboxVerified     bool
	PolicyVerdict       string
	AutoMergeAnnotation bool
	SandboxOverride     bool
	CIOverride          bool
}

// AutoMergeStatus is the public state of the auto-merge policy.
type AutoMergeStatus struct {
	Enabled        bool     `json:"enabled"`
	Namespaces     []string `json:"namespaces"`
	MinConfidence  float64  `json:"min_confidence"`
	RequireCIGreen bool     `json:"require_ci_green"`
	MaxPerHour     int      `json:"max_per_hour"`
	MergesThisHour int      `json:"merges_this_hour"`
	CooldownActive bool     `json:"cooldown_active"`
	CooldownReason string   `json:"cooldown_reason,omitempty"`
}

// PauseController is implemented by the slackbot. /sentinel pause and
// /sentinel resume use it to pause autonomous mode globally.
type PauseController interface {
	IsPaused() bool
	Pause(duration time.Duration, reason string)
	Resume()
}

// RollbackExecutor is implemented by executors.K8sExecutor.
type RollbackExecutor interface {
	RollbackByName(ctx context.Context, namespace, deploymentName string) error
}

// Store is the storage interface chatops needs (subset of feedback.Store).
type Store interface {
	FindIncidentByPR(ctx context.Context, repo string, prNumber int) (*Incident, error)
	LogCommandEvent(ctx context.Context, ev *CommandEvent) error
	UpdateOverrides(ctx context.Context, incidentID string, sandboxOverride, ciOverride bool) error
}

// Incident is a minimal incident view for command handlers. chatops keeps
// its own copy so it doesn't import schemas — the adapter in cmd/sentinel
// converts.
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

	FixType            string
	HasSchemaMigration bool
	IsHighRisk         bool
	SandboxVerified    bool
	SandboxSummary     string
	PolicyVerdict      string
	BaseBranch         string

	AutoMergeEnabled bool
	AutoMergeMerged  bool
	AutoMergeSkipped string

	AgentTrace []map[string]any

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
	// ReactComment adds an emoji reaction to a PR comment. GitHub dedupes
	// reactions from the same user — calling this twice with the same
	// reaction is a no-op (does not error).
	ReactComment(ctx context.Context, repo string, commentID int64, reaction string) error

	// RemoveReaction removes the bot's reaction of the given type.
	RemoveReaction(ctx context.Context, repo string, commentID int64, reaction string) error

	// PostComment posts a new comment on a PR.
	PostComment(ctx context.Context, repo string, prNumber int, body string) error

	// FindStatusComment returns the ID + body of the [SENTINELSTATUS]
	// comment on this PR, or 0 if not found.
	FindStatusComment(ctx context.Context, repo string, prNumber int) (commentID int64, body string, err error)

	// UpdateComment edits an existing comment in place.
	UpdateComment(ctx context.Context, repo string, commentID int64, body string) error

	// GetPermission returns the user's permission level on the repo:
	// "read", "write", "admin", "maintain", "triage", or "none".
	GetPermission(ctx context.Context, repo, username string) (string, error)

	// PostCommentWithID posts a comment and returns its ID (for follow-up
	// reactions).
	PostCommentWithID(ctx context.Context, repo string, prNumber int, body string) (int64, error)

	// SetLabels adds labels to a PR (used by /sentinel approve, /sentinel
	// hold). Labels are the gate; the auto-merger watches them.
	SetLabels(ctx context.Context, repo string, prNumber int, labels []string) error

	// RemoveLabel removes a label from a PR.
	RemoveLabel(ctx context.Context, repo string, prNumber int, label string) error

	// GetLabels returns the labels on a PR.
	GetLabels(ctx context.Context, repo string, prNumber int) ([]string, error)

	// Merge merges a PR via the GitHub API. Used by /sentinel merge (the
	// manual merge-through-Sentinel path so the outcome watcher attaches).
	Merge(ctx context.Context, repo string, prNumber int, commitMessage string) error

	// FindCommentByMarker returns the body of the first PR comment
	// containing the given marker string. Used by /sentinel apply to find
	// the [SENTINELALTERNATIVES] comment.
	FindCommentByMarker(ctx context.Context, repo string, prNumber int, marker string) (string, error)
}

// LLMRouter is the LLM interface chatops needs (for /sentinel explain,
// /sentinel alternatives). Subset of llm.Router.
type LLMRouter interface {
	FrontierText(ctx context.Context, system, user string, maxTokens int) (string, error)
	FrontierJSON(ctx context.Context, system, user string, maxTokens int) (map[string]any, error)
}

// PipelineRunner lets /sentinel retest and /sentinel investigate re-run the
// agent pipeline.
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

// ReactionsClient manages emoji reactions on PR comments. Wraps the
// GitHubClient with stateful reaction swaps (👍 → ✅ on success).
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
// that's edited in place as state changes, instead of posting new comments.
type StatusCommentManager interface {
	// Update posts (or edits) the [SENTINELSTATUS] comment on the PR.
	Update(ctx context.Context, repo string, prNumber int, incident *Incident, extra string) error

	// Render generates the markdown body for the status comment.
	Render(incident *Incident, extra string) string
}

// ErrPermissionDenied is returned by Dispatch when the author lacks
// permission. The dispatcher posts a comment with the reason — this error
// is for logging.
type ErrPermissionDenied struct {
	Author        string
	Required      Permission
	HasPermission string
}

// Error implements error.
func (e *ErrPermissionDenied) Error() string {
	return fmt.Sprintf("%s lacks %s permission (has %s)", e.Author, e.Required, e.HasPermission)
}

// ErrNotTracked is returned when the PR doesn't have an associated Sentinel
// incident.
type ErrNotTracked struct {
	PRNumber int
	PRRepo   string
}

// Error implements error.
func (e *ErrNotTracked) Error() string {
	return fmt.Sprintf("PR %s#%d is not tracked by Sentinel", e.PRRepo, e.PRNumber)
}
