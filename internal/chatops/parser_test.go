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

func TestParseCommand_TrailingPunctuation(t *testing.T) {
	//— trailing punctuation broke parsing.
	// Sentinel strips it.
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
	// Our own [SENTINELSTATUS] comment must never be parsed.
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
	cmds := ParseCommentThatIsJustProse()
	if len(cmds) != 0 {
		t.Fatalf("expected 0 commands from prose, got %d", len(cmds))
	}
}

// ParseCommentThatIsJustProse tests that a comment without /sentinel is ignored.
func ParseCommentThatIsJustProse() []*Command {
	return ParseCommand("just a regular comment with no commands", "alice", 42, "org/repo", 100)
}

func TestParseCommand_BareSentinel(t *testing.T) {
	// "/sentinel" alone should be treated as "/sentinel help"
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
	}
	for _, tt := range tests {
		got := hasPermission(tt.required, tt.actual)
		if got != tt.expected {
			t.Errorf("hasPermission(%s, %q) = %v, want %v", tt.required, tt.actual, got, tt.expected)
		}
	}
}

func TestRegistry_AllCommandsRegistered(t *testing.T) {
	// The 7 MVP commands should be registered.
	expected := []string{"help", "status", "explain", "trace", "retry-sandbox", "alternatives", "apply"}
	for _, name := range expected {
		if LookupCommand(name) == nil {
			t.Errorf("expected command %q to be registered", name)
		}
	}
}

func TestRenderHelp_FiltersByPermission(t *testing.T) {
	// A read-only user should see Informational commands without 🔒,
	// and Operational/Destructive commands WITH 🔒.
	body := RenderHelp("alice", "read")
	if body == "" {
		t.Fatal("expected non-empty help body")
	}
	// Should contain the "Informational" section
	if !contains(body, "Informational") {
		t.Error("help should contain Informational section")
	}
	// Should contain the "Operational" section with 🔒 markers
	if !contains(body, "Operational") {
		t.Error("help should contain Operational section")
	}
	if !contains(body, "🔒") {
		t.Error("help should contain 🔒 markers for locked commands")
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
