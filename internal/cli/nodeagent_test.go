package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNodeAgentCommandParsesFlags(t *testing.T) {
	root := NewRoot()
	cmd, _, err := root.Find([]string{"node-agent"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.ParseFlags([]string{
		"--endpoint", "http://sentinel-node-agent.sentinel.svc:4319",
		"--token", "secret",
		"--log-root", "/tmp/logs",
		"--cgroup-root", "/tmp/cgroup",
		"--poll", "500ms",
		"--namespaces", "shop,payments",
		"--node", "node-a",
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := cmd.Flags().GetString("endpoint"); got != "http://sentinel-node-agent.sentinel.svc:4319" {
		t.Fatalf("endpoint %q", got)
	}
	if got, _ := cmd.Flags().GetStringSlice("namespaces"); len(got) != 2 || got[0] != "shop" || got[1] != "payments" {
		t.Fatalf("namespaces %v", got)
	}
	if got, _ := cmd.Flags().GetDuration("poll"); got != 500*time.Millisecond {
		t.Fatalf("poll %s", got)
	}
}

func TestNodeAgentRequiresEndpointAndToken(t *testing.T) {
	t.Setenv("SENTINEL_NODE_AGENT_TOKEN", "")
	_ = os.Unsetenv("SENTINEL_NODE_AGENT_TOKEN")
	root := NewRoot()
	root.SetArgs([]string{"node-agent"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("want endpoint error, got %v", err)
	}

	root = NewRoot()
	root.SetArgs([]string{"node-agent", "--endpoint", "http://example"})
	err = root.Execute()
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("want token error, got %v", err)
	}
}

func TestNodeAgentHelpMentionsTheCommand(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"node-agent", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "--endpoint") || !strings.Contains(got, "SENTINEL_NODE_AGENT_TOKEN") {
		t.Fatalf("help: %s", got)
	}
}
