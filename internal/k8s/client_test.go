package k8s

import (
        "testing"
)

// TestPatchCRStatusSkipFakeNames verifies that PatchCRStatus silently skips
// when called with fake CR names from auto/manual/slack triggers.
// v3.15.0 fix — the "patch CR status failed" error appeared at the end of
// every auto-detected investigation because the CR name was "auto-detected".
func TestPatchCRStatusSkipFakeNames(t *testing.T) {
        client := &Client{} // no clientset needed — should return nil before any API call

        fakeNames := []string{"", "auto-detected", "manual", "slack"}
        for _, name := range fakeNames {
                err := client.PatchCRStatus(nil, name, "default", "resolved")
                if err != nil {
                        t.Errorf("PatchCRStatus(%q) should return nil (skip), got: %v", name, err)
                }
        }
}
