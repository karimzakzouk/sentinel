package main

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/karimzakzouk/sentinel/v3/internal/feedback"
	"github.com/karimzakzouk/sentinel/v3/internal/schemas"
)

// TestStoreAdapterFindIncidentByPrefix verifies that the storeAdapter
// (the bridge between feedback.Store and api.Store) actually has the
// FindIncidentByPrefix method and that it works. This is the exact code
// path that handleIncident uses.
//
// v3.8.4: Written after discovering that a type-assertion approach in
// handleIncident silently failed because s.store is a *storeAdapter,
// not a *feedback.Store — the assertion always returned ok=false.
func TestStoreAdapterFindIncidentByPrefix(t *testing.T) {
	dbPath := fmt.Sprintf("/tmp/sentinel-adapter-test-%d.db", os.Getpid())
	defer os.Remove(dbPath)

	store, err := feedback.New(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	incID := uuid.MustParse("91cce5a5-1234-5678-9abc-def012345678")
	incident := &schemas.Incident{
		IncidentID: incID,
		Status:     schemas.StatusResolved,
		Cluster: schemas.AnomalyCluster{
			ClusterID: uuid.New(),
			Service:   "demo-app",
			Namespace: "sim",
			Severity:  schemas.SeverityWarning,
		},
	}
	ctx := context.Background()
	if err := store.UpsertIncident(ctx, incident); err != nil {
		t.Fatalf("insert incident: %v", err)
	}

	adapter := &storeAdapter{store: store}

	// Test 1: 8-char prefix — the exact case the CLI produces
	result, err := adapter.FindIncidentByPrefix(ctx, "91cce5a5")
	if err != nil {
		t.Fatalf("8-char prefix lookup: %v", err)
	}
	if result == nil {
		t.Fatal("8-char prefix lookup: expected non-nil result, got nil — the adapter is broken")
	}

	// Test 2: 4-char prefix (minimum)
	result, err = adapter.FindIncidentByPrefix(ctx, "91cc")
	if err != nil {
		t.Fatalf("4-char prefix lookup: %v", err)
	}
	if result == nil {
		t.Fatal("4-char prefix lookup: expected non-nil result, got nil")
	}

	// Test 3: 3-char prefix (below minimum — should return nil, not error)
	result, err = adapter.FindIncidentByPrefix(ctx, "91c")
	if err != nil {
		t.Fatalf("3-char prefix lookup: expected nil, got error: %v", err)
	}
	if result != nil {
		t.Fatal("3-char prefix lookup: expected nil (below 4-char minimum), got non-nil")
	}

	// Test 4: Non-matching prefix
	result, err = adapter.FindIncidentByPrefix(ctx, "aaaaaaaa")
	if err != nil {
		t.Fatalf("non-matching prefix lookup: %v", err)
	}
	if result != nil {
		t.Fatal("non-matching prefix lookup: expected nil, got non-nil")
	}
}

// TestStoreAdapterGetIncidentNilInterface verifies that GetIncident returns
// a true nil interface (not a typed nil *schemas.Incident boxed into a non-nil
// any) when the incident doesn't exist. Without this, handleIncident's
// `if incident == nil` check fails and the server returns 200 with body
// "null" instead of 404.
//
// v3.8.4: This is a pre-existing bug from v3.3.1 that was never caught
// because mock servers always return real incidents.
func TestStoreAdapterGetIncidentNilInterface(t *testing.T) {
	dbPath := fmt.Sprintf("/tmp/sentinel-get-nil-test-%d.db", os.Getpid())
	defer os.Remove(dbPath)

	store, err := feedback.New(dbPath)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	adapter := &storeAdapter{store: store}
	ctx := context.Background()

	// Lookup a non-existent UUID
	nonexistentID := uuid.MustParse("00000000-0000-0000-0000-000000000000")
	result, err := adapter.GetIncident(ctx, nonexistentID)
	if err != nil {
		t.Fatalf("GetIncident non-existent: expected nil error, got: %v", err)
	}
	// This is the key check: result must be a TRUE nil interface, not a
	// typed nil *schemas.Incident boxed into a non-nil any. The Go
	// nil-interface trap would make this fail without the explicit nil
	// check in the adapter.
	if result != nil {
		t.Fatalf("GetIncident non-existent: expected nil interface, got non-nil (type %T, value %v) — nil-interface trap", result, result)
	}
}
