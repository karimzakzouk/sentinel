package chatops

import (
	"testing"
)

func TestParseCommand_BasicCommand(t *testing.T) {
	cmds := ParseCommand("/sentinel help", "alice", 42, "org/repo", 100)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "help" {
		t.Errorf("expected name=help, got %q", cmds[0].Name)
	}
	if cmds[0].Author != "alice" {
		t.Errorf("expected author=alice, got %q", cmds[0].Author)
	}
	if cmds[0].PRNumber != 42 {
		t.Errorf("expected pr=42, got %d", cmds[0].PRNumber)
	}
}

func TestParseCommand_WithArgs(t *testing.T) {
	cmds := ParseCommand("/sentinel verify /pay /health", "alice", 42, "org/repo", 100)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "verify" {
		t.Errorf("expected name=verify, got %q", cmds[0].Name)
	}
	if len(cmds[0].ArgList) != 2 {
		t.Errorf("expected 2 args, got %d", len(cmds[0].ArgList))
	}
	if cmds[0].ArgList[0] != "/pay" || cmds[0].ArgList[1] != "/health" {
		t.Errorf("expected [/pay /health], got %v", cmds[0].ArgList)
	}
}

func TestParseCommand_CancelNegation(t *testing.T) {
	cmds := ParseCommand("/sentinel hold cancel", "alice", 42, "org/repo", 100)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "hold" {
		t.Errorf("expected name=hold, got %q", cmds[0].Name)
	}
	if !cmds[0].Cancel {
		t.Errorf("expected Cancel=true")
	}
}

// TestParseCommand_CancelWithExtraArgs verifies that "cancel" can be
// followed by extra args (used by /sentinel verify cancel <paths> if it
// ever needs to).
func TestParseCommand_CancelWithExtraArgs(t *testing.T) {
	cmds := ParseCommand("/sentinel verify cancel /pay", "alice", 42, "org/repo", 100)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if !cmds[0].Cancel {
		t.Error("expected Cancel=true")
	}
	if len(cmds[0].ArgList) != 1 || cmds[0].ArgList[0] != "/pay" {
		t.Errorf("expected [/pay] after cancel stripped, got %v", cmds[0].ArgList)
	}
}

func TestParseCommand_TrailingPunctuation(t *testing.T) {
	cmds := ParseCommand("/sentinel help.", "alice", 42, "org/repo", 100)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "help" {
		t.Errorf("expected name=help, got %q", cmds[0].Name)
	}
}

func TestParseCommand_MentionPrefix(t *testing.T) {
	cmds := ParseCommand("@sentinel-bot help", "alice", 42, "org/repo", 100)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "help" {
		t.Errorf("expected name=help, got %q", cmds[0].Name)
	}
}

// TestParseCommand_AtSentinelMention verifies the @sentinel prefix (without
// the -bot suffix) also works.
func TestParseCommand_AtSentinelMention(t *testing.T) {
	cmds := ParseCommand("@sentinel status", "alice", 42, "org/repo", 100)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "status" {
		t.Errorf("expected name=status, got %q", cmds[0].Name)
	}
}

func TestParseCommand_MultipleCommandsPerComment(t *testing.T) {
	cmds := ParseCommand("/sentinel help\n/sentinel status", "alice", 42, "org/repo", 100)
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}
	if cmds[0].Name != "help" || cmds[1].Name != "status" {
		t.Errorf("expected [help status], got [%s %s]", cmds[0].Name, cmds[1].Name)
	}
}

func TestParseCommand_IgnoresStatusComment(t *testing.T) {
	body := `<!-- [SENTINELSTATUS] incident_id=abc -->
## Status
| Phase | Status |
/sentinel help`
	cmds := ParseCommand(body, "sentinel-bot", 42, "org/repo", 100)
	if len(cmds) != 0 {
		t.Fatalf("expected 0 commands from status comment, got %d", len(cmds))
	}
}

func TestParseCommand_NoCommand(t *testing.T) {
	cmds := ParseCommand("just a regular comment with no commands", "alice", 42, "org/repo", 100)
	if len(cmds) != 0 {
		t.Fatalf("expected 0 commands from prose, got %d", len(cmds))
	}
}

func TestParseCommand_BareSentinel(t *testing.T) {
	// "/sentinel" alone should be treated as "/sentinel help".
	cmds := ParseCommand("/sentinel", "alice", 42, "org/repo", 100)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "help" {
		t.Errorf("expected name=help for bare /sentinel, got %q", cmds[0].Name)
	}
}

func TestParseCommand_CaseInsensitive(t *testing.T) {
	cmds := ParseCommand("/Sentinel HELP", "alice", 42, "org/repo", 100)
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "help" {
		t.Errorf("expected name=help (lowercased), got %q", cmds[0].Name)
	}
}

func TestHasPermission(t *testing.T) {
	tests := []struct {
		required Permission
		actual   string
		expected bool
	}{
		{PermRead, "read", true},
		{PermRead, "write", true},
		{PermRead, "admin", true},
		{PermWrite, "read", false},
		{PermWrite, "write", true},
		{PermWrite, "admin", true},
		{PermAdmin, "read", false},
		{PermAdmin, "write", false},
		{PermAdmin, "admin", true},
		{PermRead, "none", false},
		{PermRead, "", false},
		// maintain satisfies write tier; triage satisfies read tier.
		{PermWrite, "maintain", true},
		{PermAdmin, "maintain", false},
		{PermRead, "triage", true},
		{PermWrite, "triage", false},
	}
	for _, tt := range tests {
		got := hasPermission(tt.required, tt.actual)
		if got != tt.expected {
			t.Errorf("hasPermission(%s, %q) = %v, want %v", tt.required, tt.actual, got, tt.expected)
		}
	}
}

// TestRegistry_AllCommandsRegistered verifies that every /sentinel command
// is registered (the 7 in commands.go + 12 in commands_more.go).
func TestRegistry_AllCommandsRegistered(t *testing.T) {
	expected := []string{
		// Informational
		"help", "status", "explain", "trace",
		// Operational (MVP)
		"retry-sandbox", "alternatives", "apply",
		// Operational (more)
		"hold", "approve", "no-merge", "merge", "investigate", "retest", "verify",
		// Destructive
		"revert", "rollback", "pause", "resume", "override",
	}
	for _, name := range expected {
		if LookupCommand(name) == nil {
			t.Errorf("expected command %q to be registered", name)
		}
	}
	if got := len(AllCommands()); got != len(expected) {
		t.Errorf("expected %d commands registered, got %d", len(expected), got)
	}
}

// TestRegistry_PermissionLookup verifies that LookupCommand returns the
// correct Permission tier for each command.
func TestRegistry_PermissionLookup(t *testing.T) {
	cases := []struct {
		name     string
		wantPerm Permission
		wantTier string
	}{
		{"help", PermRead, "Informational"},
		{"status", PermRead, "Informational"},
		{"explain", PermRead, "Informational"},
		{"trace", PermRead, "Informational"},
		{"retry-sandbox", PermWrite, "Operational"},
		{"alternatives", PermWrite, "Operational"},
		{"apply", PermWrite, "Operational"},
		{"hold", PermWrite, "Operational"},
		{"approve", PermWrite, "Operational"},
		{"no-merge", PermWrite, "Operational"},
		{"merge", PermWrite, "Operational"},
		{"investigate", PermWrite, "Operational"},
		{"retest", PermWrite, "Operational"},
		{"verify", PermWrite, "Operational"},
		{"revert", PermAdmin, "Destructive"},
		{"rollback", PermAdmin, "Destructive"},
		{"pause", PermAdmin, "Destructive"},
		{"resume", PermAdmin, "Destructive"},
		{"override", PermAdmin, "Destructive"},
	}
	for _, c := range cases {
		spec := LookupCommand(c.name)
		if spec == nil {
			t.Errorf("%s: command not registered", c.name)
			continue
		}
		if spec.Permission != c.wantPerm {
			t.Errorf("%s: Permission=%s, want %s", c.name, spec.Permission, c.wantPerm)
		}
		if spec.Tier != c.wantTier {
			t.Errorf("%s: Tier=%q, want %q", c.name, spec.Tier, c.wantTier)
		}
		if spec.Handler == nil {
			t.Errorf("%s: Handler is nil", c.name)
		}
	}
}

// TestRegistry_LookupCommandCaseInsensitive verifies LookupCommand lower-
// cases the input.
func TestRegistry_LookupCommandCaseInsensitive(t *testing.T) {
	if LookupCommand("HELP") == nil {
		t.Error("LookupCommand should be case-insensitive")
	}
	if LookupCommand("Help") == nil {
		t.Error("LookupCommand should be case-insensitive")
	}
}

// TestRegistry_CancelableFlags verifies the Cancelable flag is set on
// state-changing commands that support `cancel`.
func TestRegistry_CancelableFlags(t *testing.T) {
	cancelable := []string{"hold", "approve", "no-merge", "merge", "verify"}
	for _, name := range cancelable {
		spec := LookupCommand(name)
		if spec == nil {
			t.Errorf("%s: not registered", name)
			continue
		}
		if !spec.Cancelable {
			t.Errorf("%s: expected Cancelable=true", name)
		}
	}
	notCancelable := []string{"help", "status", "explain", "trace", "investigate", "retest", "revert", "rollback", "pause", "resume", "override"}
	for _, name := range notCancelable {
		spec := LookupCommand(name)
		if spec == nil {
			t.Errorf("%s: not registered", name)
			continue
		}
		if spec.Cancelable {
			t.Errorf("%s: expected Cancelable=false", name)
		}
	}
}

func TestRenderHelp_FiltersByPermission(t *testing.T) {
	body := RenderHelp("alice", "read")
	if body == "" {
		t.Fatal("expected non-empty help body")
	}
	if !contains(body, "Informational") {
		t.Error("help should contain Informational section")
	}
	if !contains(body, "Operational") {
		t.Error("help should contain Operational section")
	}
	if !contains(body, "🔒") {
		t.Error("help should contain 🔒 markers for locked commands")
	}
}

// TestRenderHelp_AdminSeesAll verifies an admin sees no 🔒 markers.
func TestRenderHelp_AdminSeesAll(t *testing.T) {
	body := RenderHelp("root", "admin")
	if contains(body, "🔒") {
		t.Error("admin should see no 🔒 markers")
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
