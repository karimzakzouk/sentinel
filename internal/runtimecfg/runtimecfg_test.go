package runtimecfg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadNonexistentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	mgr := New(path)
	mgr.Load()

	cfg := mgr.Get()
	if cfg == nil {
		t.Fatal("Get() returned nil for nonexistent file")
	}
}

func TestSaveFromJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-config.json")
	mgr := New(path)
	mgr.Load()

	newCfg := RuntimeConfig{
		LLMProvider:      "openrouter",
		LLMFastModel:     "gpt-4o-mini",
		LLMFrontierModel: "gpt-4o",
		LogLevel:         "debug",
	}
	jsonBytes, _ := json.Marshal(newCfg)

	if err := mgr.SaveFromJSON(jsonBytes); err != nil {
		t.Fatalf("SaveFromJSON error: %v", err)
	}

	loaded := mgr.Get()
	if loaded.LLMProvider != "openrouter" {
		t.Errorf("LLMProvider: got %q, want openrouter", loaded.LLMProvider)
	}
	if loaded.LLMFastModel != "gpt-4o-mini" {
		t.Errorf("LLMFastModel: got %q", loaded.LLMFastModel)
	}
	if loaded.LogLevel != "debug" {
		t.Errorf("LogLevel: got %q, want DEBUG", loaded.LogLevel)
	}
}

func TestSaveFromInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-config.json")
	mgr := New(path)
	mgr.Load()

	if err := mgr.SaveFromJSON([]byte("not json")); err == nil {
		t.Error("SaveFromJSON should fail on invalid JSON")
	}
}

func TestPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-config.json")

	mgr1 := New(path)
	mgr1.Load()

	newCfg := RuntimeConfig{
		LLMProvider:  "anthropic",
		LLMFastModel: "claude-3-5-sonnet",
	}
	jsonBytes, _ := json.Marshal(newCfg)
	if err := mgr1.SaveFromJSON(jsonBytes); err != nil {
		t.Fatalf("SaveFromJSON error: %v", err)
	}

	// Create a new manager from the same path — should load the saved config.
	mgr2 := New(path)
	mgr2.Load()

	loaded := mgr2.Get()
	if loaded.LLMProvider != "anthropic" {
		t.Errorf("LLMProvider after reload: got %q, want anthropic", loaded.LLMProvider)
	}
}

func TestSaveDirect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-config.json")
	mgr := New(path)
	mgr.Load()

	cfg := &RuntimeConfig{
		LLMProvider:  "gemini",
		LLMFastModel: "gemini-1.5-flash",
		LogLevel:     "info",
	}

	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("config file was not created")
	}

	loaded := mgr.Get()
	if loaded.LLMProvider != "gemini" {
		t.Errorf("LLMProvider: got %q, want gemini", loaded.LLMProvider)
	}
}

func TestSetApplyCallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-config.json")
	mgr := New(path)
	mgr.Load()

	called := false
	mgr.SetApplyCallback(func(cfg *RuntimeConfig) {
		called = true
		if cfg.LLMProvider != "groq" {
			t.Errorf("callback got LLMProvider %q, want groq", cfg.LLMProvider)
		}
	})

	newCfg := RuntimeConfig{LLMProvider: "groq"}
	jsonBytes, _ := json.Marshal(newCfg)
	if err := mgr.SaveFromJSON(jsonBytes); err != nil {
		t.Fatalf("SaveFromJSON error: %v", err)
	}

	if !called {
		t.Error("apply callback was not called after SaveFromJSON")
	}
}

// TestRuntimeCfg_HotReload verifies that a config change applied via Save
// triggers the apply callback synchronously — i.e. the change takes effect
// on live components without a restart. A counter is used so we can assert
// the callback fired exactly the expected number of times across multiple
// saves.
func TestRuntimeCfg_HotReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-config.json")
	mgr := New(path)
	mgr.Load()

	// Track the latest values the apply callback saw.
	var applied atomic.Value
	applied.Store(RuntimeConfig{})
	var count atomic.Int32
	mgr.SetApplyCallback(func(cfg *RuntimeConfig) {
		applied.Store(*cfg)
		count.Add(1)
	})

	// Save #1: change the LLM key.
	if err := mgr.SaveFromJSON([]byte(`{"llm_api_key":"key-1","llm_fast_model":"m1"}`)); err != nil {
		t.Fatalf("save #1: %v", err)
	}
	got := applied.Load().(RuntimeConfig)
	if got.LLMAPIKey != "key-1" {
		t.Errorf("after save #1: LLMAPIKey = %q, want key-1", got.LLMAPIKey)
	}
	if got.LLMFastModel != "m1" {
		t.Errorf("after save #1: LLMFastModel = %q, want m1", got.LLMFastModel)
	}

	// Save #2: change confidence threshold + production namespaces.
	if err := mgr.SaveFromJSON([]byte(`{"confidence_threshold_autonomous":0.95,"production_namespaces":"prod,prod-eu"}`)); err != nil {
		t.Fatalf("save #2: %v", err)
	}
	got = applied.Load().(RuntimeConfig)
	if got.ConfidenceAutonomous != 0.95 {
		t.Errorf("after save #2: ConfidenceAutonomous = %v, want 0.95", got.ConfidenceAutonomous)
	}
	if got.ProductionNamespaces != "prod,prod-eu" {
		t.Errorf("after save #2: ProductionNamespaces = %q, want prod,prod-eu", got.ProductionNamespaces)
	}

	// Save #3: invalid config — callback must NOT fire.
	err := mgr.SaveFromJSON([]byte(`{"investigator_timeout_sec":-5}`))
	if err == nil {
		t.Fatal("expected validation error on negative timeout")
	}
	if count.Load() != 2 {
		t.Errorf("after 3 saves (1 invalid): callback count = %d, want 2", count.Load())
	}

	// The current in-memory config should still reflect save #2.
	cur := mgr.Get()
	if cur.ConfidenceAutonomous != 0.95 {
		t.Errorf("current config: ConfidenceAutonomous = %v, want 0.95", cur.ConfidenceAutonomous)
	}
}

// TestValidate verifies Validate() rejects out-of-range thresholds and
// negative timeouts.
func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     RuntimeConfig
		wantErr string
	}{
		{"negative investigator timeout", RuntimeConfig{InvestigatorTimeoutSec: -1}, "investigator_timeout_sec"},
		{"confidence loopback > 1", RuntimeConfig{ConfidenceLoopBackThreshold: 1.5}, "confidence_loop_back_threshold"},
		{"autonomous confidence < 0", RuntimeConfig{ConfidenceAutonomous: -0.1}, "confidence_threshold_autonomous"},
		{"llm retries > 10", RuntimeConfig{LLMMaxRetries: 20}, "llm_max_retries"},
		{"invalid log level", RuntimeConfig{LogLevel: "trace"}, "log_level"},
		{"valid config", RuntimeConfig{LogLevel: "info", InvestigatorTimeoutSec: 60}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.cfg.Validate()
			if tc.wantErr == "" {
				if len(errs) != 0 {
					t.Errorf("expected no errors, got %v", errs)
				}
				return
			}
			found := false
			for _, e := range errs {
				if containsStr(e, tc.wantErr) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected error containing %q, got %v", tc.wantErr, errs)
			}
		})
	}
}

// TestClone verifies Clone deep-copies pointer fields so mutating the
// clone does not affect the original.
func TestClone(t *testing.T) {
	original := RuntimeConfig{SkipSandboxNonProd: boolPtr(true), SandboxEnabled: boolPtr(false)}
	clone := original.Clone()
	*clone.SkipSandboxNonProd = false
	*clone.SandboxEnabled = true
	if *original.SkipSandboxNonProd != true {
		t.Error("Clone did not deep-copy SkipSandboxNonProd")
	}
	if *original.SandboxEnabled != false {
		t.Error("Clone did not deep-copy SandboxEnabled")
	}
}

// TestSetGitHubApp verifies SetGitHubApp persists + triggers the callback.
func TestSetGitHubApp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-config.json")
	mgr := New(path)

	var applied atomic.Value
	applied.Store(RuntimeConfig{})
	mgr.SetApplyCallback(func(cfg *RuntimeConfig) {
		applied.Store(*cfg)
	})

	slug, err := mgr.SetGitHubApp("123", "PRIVATE-KEY", "456", "secret")
	if err != nil {
		t.Fatalf("SetGitHubApp: %v", err)
	}
	if slug == "" {
		t.Error("expected non-empty slug placeholder")
	}
	got := applied.Load().(RuntimeConfig)
	if got.GitHubAppID != "123" || got.GitHubAppPrivateKey != "PRIVATE-KEY" || got.GitHubAppInstallID != "456" {
		t.Errorf("applied config: %+v", got)
	}

	status := mgr.GetGitHubAppStatus()
	if !status.Configured || status.AppID != "123" {
		t.Errorf("GetGitHubAppStatus: %+v", status)
	}

	if err := mgr.RemoveGitHubApp(); err != nil {
		t.Fatalf("RemoveGitHubApp: %v", err)
	}
	if mgr.GetGitHubAppStatus().Configured {
		t.Error("RemoveGitHubApp should clear Configured")
	}
}

// TestRunConfigMapPollerNilClient verifies RunConfigMapPoller does not
// panic when the k8s client is nil (it logs and returns).
func TestRunConfigMapPollerNilClient(t *testing.T) {
	mgr := New("")
	// Should not block or panic.
	mgr.RunConfigMapPoller(context.TODO(), nil, ConfigMapSource{Name: "x", Namespace: "y", Key: "z"})
	// Give the goroutine (if it started) a moment to no-op.
	time.Sleep(10 * time.Millisecond)
}

// TestPollConfigMapOnceMissingKey verifies pollConfigMapOnce returns an
// error when the named key is absent from the ConfigMap. We don't have a
// fake K8s client wired up here, so this test focuses on the Get path:
// calling with a nil interface returns an error from the K8s client.
func TestPollConfigMapOnceNilClient(t *testing.T) {
	_, err := pollConfigMapOnce(context.TODO(), nil, ConfigMapSource{Name: "x", Namespace: "y", Key: "z"})
	if err == nil {
		t.Error("expected error with nil k8s client")
	}
}

func boolPtr(b bool) *bool { return &b }

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
