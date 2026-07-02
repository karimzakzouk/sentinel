package runtimecfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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

	// Create a new manager from the same path — should load the saved config
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
		LLMProvider:   "gemini",
		LLMFastModel:  "gemini-1.5-flash",
		LogLevel:      "info",
	}

	if err := mgr.Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Verify file exists
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
