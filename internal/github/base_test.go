package github

import (
	"encoding/json"
	"strings"
	"testing"
)

// Run #28: the revert-commit payload marshalled "parents" as a JSON
// STRING ("[\"sha\"]"), GitHub rejected it with 422, and the auto-revert
// of a regressed merge silently never happened. The typed struct must
// keep parents a real array.
func TestRevertCommitPayloadParentsIsArray(t *testing.T) {
	b, err := json.Marshal(revertCommitPayload{
		Message: "revert: abc",
		Tree:    "tree-sha",
		Parents: []string{"head-sha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"parents":["head-sha"]`) {
		t.Errorf("parents must marshal as a JSON array, got %s", s)
	}
	if strings.Contains(s, `"parents":"`) {
		t.Errorf("parents must NOT be a string, got %s", s)
	}
}
