package chatops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

var (
	registryMu sync.RWMutex
	registry   = map[string]*CommandSpec{}
)

// RegisterCommand adds a command to the global registry. Called from init()
// blocks in commands*.go. Panics on duplicate name (programming error, not
// runtime).
func RegisterCommand(spec *CommandSpec) {
	registryMu.Lock()
	defer registryMu.Unlock()
	name := strings.ToLower(spec.Name)
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("chatops: duplicate command registration: %s", name))
	}
	registry[name] = spec
}

// LookupCommand returns the spec for a command name, or nil if not found.
func LookupCommand(name string) *CommandSpec {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[strings.ToLower(name)]
}

// AllCommands returns a snapshot of all registered commands, sorted by tier
// (Informational, Operational, Destructive) then by name. Used by
// /sentinel help.
func AllCommands() []*CommandSpec {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]*CommandSpec, 0, len(registry))
	for _, c := range registry {
		out = append(out, c)
	}
	tierOrder := map[string]int{
		"Informational": 0,
		"Operational":   1,
		"Destructive":   2,
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := tierOrder[out[i].Tier], tierOrder[out[j].Tier]
		if ti != tj {
			return ti < tj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Dispatch executes one or more parsed commands from a single comment. For
// each command:
//  1. ACK with 👍 reaction.
//  2. Look up the command in the registry → 404 comment if unknown.
//  3. Check the author's permission on the repo.
//  4. If denied → post actionable error comment, log to audit, continue.
//  5. Otherwise → execute the handler.
//  6. Swap reaction to ✅ on success or ❌ on failure.
//  7. Log to command_events audit table.
//
// Dispatch never returns an error for "soft" failures (unknown command,
// permission denied, PR not tracked) — those are posted as comments. It
// only returns an error for infrastructure failures.
func Dispatch(ctx context.Context, commands []*Command, deps *Deps) error {
	for _, cmd := range commands {
		if err := dispatchOne(ctx, cmd, deps); err != nil {
			// Log + continue with the next command. Don't fail the whole batch.
			logging.Error("chatops: dispatch error",
				"command", cmd.Name, "author", cmd.Author,
				"repo", cmd.PRRepo, "pr", cmd.PRNumber, "err", err)
		}
	}
	return nil
}

// dispatchOne executes a single command end-to-end.
func dispatchOne(ctx context.Context, cmd *Command, deps *Deps) error {
	// 1. Immediate ACK — 👍 reaction.
	if deps.Reactions != nil {
		_ = deps.Reactions.Ack(ctx, cmd.PRRepo, cmd.CommentID)
	}

	// 2. Look up command in registry.
	spec := LookupCommand(cmd.Name)
	if spec == nil {
		body := fmt.Sprintf("Unknown command `/sentinel %s`. Run `/sentinel help` to see available commands.", cmd.Name)
		_ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, body)
		if deps.Reactions != nil {
			_ = deps.Reactions.Failure(ctx, cmd.PRRepo, cmd.CommentID)
		}
		_ = auditLog(ctx, deps, cmd, "unknown_command", "")
		return nil
	}

	// 3. Check author permission on the repo.
	perm, err := deps.GitHub.GetPermission(ctx, cmd.PRRepo, cmd.Author)
	if err != nil {
		// GitHub API failure — can't verify permission. Fail safe: deny.
		body := fmt.Sprintf("⚠️ Could not verify @%s's permission on %s (GitHub API error: %v).\nRefusing to execute for safety. Try again in a minute.", cmd.Author, cmd.PRRepo, err)
		_ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, body)
		if deps.Reactions != nil {
			_ = deps.Reactions.Failure(ctx, cmd.PRRepo, cmd.CommentID)
		}
		_ = auditLog(ctx, deps, cmd, "permission_check_failed", err.Error())
		return nil
	}

	if !hasPermission(spec.Permission, perm) {
		body := actionablePermissionError(cmd, spec, perm)
		_ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, body)
		if deps.Reactions != nil {
			_ = deps.Reactions.Failure(ctx, cmd.PRRepo, cmd.CommentID)
		}
		_ = auditLog(ctx, deps, cmd, "denied", fmt.Sprintf("required=%s has=%s", spec.Permission, perm))
		return nil
	}

	// 4. Execute the handler.
	err = spec.Handler(ctx, cmd, deps)
	if err != nil {
		if deps.Reactions != nil {
			_ = deps.Reactions.Failure(ctx, cmd.PRRepo, cmd.CommentID)
		}
		_ = auditLog(ctx, deps, cmd, "error", err.Error())
		body := fmt.Sprintf("❌ `/sentinel %s` failed: %v", cmd.Name, err)
		_ = deps.GitHub.PostComment(ctx, cmd.PRRepo, cmd.PRNumber, body)
		return nil
	}

	// 5. Success — swap reaction.
	if deps.Reactions != nil {
		_ = deps.Reactions.Success(ctx, cmd.PRRepo, cmd.CommentID)
	}
	_ = auditLog(ctx, deps, cmd, "success", "")
	return nil
}

// hasPermission reports whether the user's actual permission satisfies the
// command's required permission. "admin" satisfies all tiers; "write" and
// "maintain" satisfy PermRead+PermWrite; "read" and "triage" satisfy only
// PermRead.
func hasPermission(required Permission, actual string) bool {
	switch strings.ToLower(actual) {
	case "admin":
		return true
	case "write", "maintain":
		return required <= PermWrite
	case "read", "triage":
		return required <= PermRead
	default:
		return false
	}
}

// actionablePermissionError builds a comment body that explains *why* the
// user was denied and *what they can do about it*.
func actionablePermissionError(cmd *Command, spec *CommandSpec, has string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "❌ @%s doesn't have the required permission to run `/sentinel %s`.\n\n", cmd.Author, cmd.Name)

	fmt.Fprintf(&sb, "**Required:** %s access to `%s`\n", spec.Permission, cmd.PRRepo)

	fmt.Fprintf(&sb, "**Your access:** %s\n\n", has)

	switch spec.Permission {
	case PermWrite:
		sb.WriteString("To run this command, you need:\n")
		sb.WriteString("- **Write access** to this repo, OR\n")
		sb.WriteString("- Be a member of the team configured in the `sentinel.io/admin-team` annotation on the service's Deployment\n\n")
		fmt.Fprintf(&sb, "Request access: https://github.com/%s/settings/access\n", cmd.PRRepo)

		sb.WriteString("Or ask a collaborator to run this command for you.\n")
	case PermAdmin:
		sb.WriteString("To run this destructive command, you need:\n")
		sb.WriteString("- **Admin access** to this repo, OR\n")
		sb.WriteString("- Be a member of the team configured in the `sentinel.io/admin-team` annotation on the service's Deployment\n\n")
		sb.WriteString("Destructive commands (revert, rollback, pause, override) require elevated trust because they touch production state.\n")
	default:
		sb.WriteString("This command should be available to all collaborators. If you're seeing this in error, please file a bug.\n")
	}
	return sb.String()
}

// auditLog writes a command event to the audit table (best-effort).
func auditLog(ctx context.Context, deps *Deps, cmd *Command, result, detail string) error {
	if deps.Store == nil {
		return nil
	}
	incidentID := ""
	if inc, _ := deps.Store.FindIncidentByPR(ctx, cmd.PRRepo, cmd.PRNumber); inc != nil {
		incidentID = inc.ID
	}
	ev := &CommandEvent{
		IncidentID:       incidentID,
		PRNumber:         cmd.PRNumber,
		PRRepo:           cmd.PRRepo,
		Command:          cmd.Name,
		Args:             cmd.Args,
		Author:           cmd.Author,
		AuthorPermission: "",
		Result:           result,
		ResultDetail:     detail,
		Timestamp:        cmd.Timestamp,
	}
	return deps.Store.LogCommandEvent(ctx, ev)
}

// RenderHelp generates the markdown body for /sentinel help, filtered by
// what the requesting user can actually run. Higher-tier commands the user
// can't run are shown with a 🔒 marker so the user knows they exist.
func RenderHelp(author string, userPermission string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Available commands for @%s on this PR\n\n", author)

	groups := []struct {
		tier  string
		title string
		desc  string
	}{
		{"Informational", "Informational", fmt.Sprintf("Available to anyone with read access (you have: %s).", userPermission)},
		{"Operational", "Operational", "Require write access. You can run these if you have write+ access."},
		{"Destructive", "Destructive", "Require admin access. Commands you cannot run are prefixed with a lock."},
	}

	for _, g := range groups {
		commands := commandsInTier(g.tier)
		if len(commands) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "### %s\n\n%s\n\n", g.title, g.desc)

		for _, c := range commands {
			locked := ""
			if !hasPermission(c.Permission, userPermission) {
				locked = "🔒 "
			}
			fmt.Fprintf(&sb, "%s`%s` — %s\n", locked, c.Usage, c.Description)

		}
		sb.WriteString("\n")
	}

	sb.WriteString("_Tip: every state-changing command takes `cancel` to undo. E.g. `/sentinel hold cancel`._\n")
	return sb.String()
}

// commandsInTier returns all registered commands in the given tier, sorted
// by name (matches AllCommands order).
func commandsInTier(tier string) []*CommandSpec {
	all := AllCommands()
	out := make([]*CommandSpec, 0)
	for _, c := range all {
		if c.Tier == tier {
			out = append(out, c)
		}
	}
	return out
}
