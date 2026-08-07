package attest

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestAttest_UUIDv7Format verifies the textual format of newUUIDv7:
//   - 36 chars total (8-4-4-4-12 with dashes)
//   - char 14 (version nibble) is '7'
//   - char 19 (variant nibble) is one of '8', '9', 'a', 'b'
func TestAttest_UUIDv7Format(t *testing.T) {
	for i := 0; i < 1000; i++ {
		u := newUUIDv7()
		if len(u) != 36 {
			t.Fatalf("len = %d, want 36 (uuid=%q)", len(u), u)
		}
		parts := strings.Split(u, "-")
		if len(parts) != 5 {
			t.Fatalf("expected 5 dash-separated groups, got %d (uuid=%q)", len(parts), u)
		}
		wantLens := []int{8, 4, 4, 4, 12}
		for j, p := range parts {
			if len(p) != wantLens[j] {
				t.Fatalf("group %d len = %d, want %d (uuid=%q)", j, len(p), wantLens[j], u)
			}
		}
		// Char 14 is the first char of the 3rd group (version nibble).
		if u[14] != '7' {
			t.Errorf("version nibble = %q, want '7' (uuid=%q)", u[14], u)
		}
		// Char 19 is the first char of the 4th group (variant nibble).
		// Variant 0b10 means the top 2 bits of the byte are 10, so the
		// hex digit is 8, 9, a, or b.
		switch u[19] {
		case '8', '9', 'a', 'b':
		default:
			t.Errorf("variant nibble = %q, want one of 8/9/a/b (uuid=%q)", u[19], u)
		}
	}
}

// TestAttest_UUIDv7IsTimeOrdered is the regression test for the v5 bug
// where newUUIDv7 produced correlated random bits instead of a real
// RFC 9562 UUIDv7. A real UUIDv7 is lexicographically sortable by
// generation time (because the top 48 bits are the Unix ms timestamp).
//
// We generate 100 UUIDs with at least 1ms between each, then verify
// they sort in the order they were generated.
func TestAttest_UUIDv7IsTimeOrdered(t *testing.T) {
	const n = 100
	uuids := make([]string, n)
	for i := range uuids {
		uuids[i] = newUUIDv7()
		time.Sleep(time.Millisecond)
	}

	sorted := make([]string, n)
	copy(sorted, uuids)
	sort.Strings(sorted)

	for i := range uuids {
		if uuids[i] != sorted[i] {
			t.Fatalf("UUIDv7 not time-ordered at index %d:\n  generated[%d]=%s\n  sorted[%d]   =%s",
				i, i, uuids[i], i, sorted[i])
		}
	}
}

// TestAttest_UUIDv7TimestampMatches verifies the top 48 bits of the UUID
// actually encode the generation time (within 1 second). This is the
// property that makes UUIDv7 sortable.
func TestAttest_UUIDv7TimestampMatches(t *testing.T) {
	before := time.Now().UTC().UnixMilli()
	u := newUUIDv7()
	after := time.Now().UTC().UnixMilli()

	// First 12 hex chars = 48 bits = Unix ms timestamp.
	tsHex := strings.Split(u, "-")[0] + strings.Split(u, "-")[1]
	var ts uint64
	for _, c := range tsHex {
		ts = ts<<4 | uint64(strings.IndexByte("0123456789abcdef", byte(c)))
	}
	if ts < uint64(before) || ts > uint64(after) {
		t.Errorf("UUIDv7 timestamp %d not in [%d, %d] (uuid=%s)", ts, before, after, u)
	}
}

// TestAttest_SignatureVerifies signs one attestation and verifies it
// with the public key.
func TestAttest_SignatureVerifies(t *testing.T) {
	s, err := NewSigner("", t.TempDir())
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	a := Attestation{
		Action:     "investigation_started",
		IncidentID: "inc-123",
		Reason:     "pod crash-loop detected",
	}
	if err := s.Sign(context.Background(), a); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Read the signed attestation back from the log file.
	entries := readLog(t, s)
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	signed := entries[0]
	if signed.UUID == "" {
		t.Error("expected UUID to be set")
	}
	if signed.Signature == "" {
		t.Error("expected Signature to be set")
	}
	if signed.Actor != "sentinel" {
		t.Errorf("expected Actor='sentinel', got %q", signed.Actor)
	}

	// Verify signature with the public key. PreviousHash is "" (first attestation).
	if err := Verify(signed, s.PublicKey(), ""); err != nil {
		t.Errorf("Verify: %v", err)
	}

	// Tampering with the action must break verification.
	signed.Action = "pr_merged"
	if err := Verify(signed, s.PublicKey(), ""); err == nil {
		t.Error("Verify should fail on tampered action")
	}
}

// TestAttest_HashChainLinks signs N attestations in sequence and
// verifies each one's PreviousHash matches the SHA-256 of the previous
// signed attestation. Also verifies the chain via VerifyLog.
func TestAttest_HashChainLinks(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSigner("", dir)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	actions := []string{"investigation_started", "fix_proposed", "pr_opened", "pr_merged"}
	for _, a := range actions {
		if err := s.Sign(context.Background(), Attestation{Action: a, IncidentID: "inc-chain"}); err != nil {
			t.Fatalf("Sign %s: %v", a, err)
		}
	}

	entries := readLog(t, s)
	if len(entries) != len(actions) {
		t.Fatalf("expected %d entries, got %d", len(actions), len(entries))
	}

	var expectedPrev string
	for i, e := range entries {
		if e.PreviousHash != expectedPrev {
			t.Fatalf("entry %d: PreviousHash=%s, want %s", i, e.PreviousHash, expectedPrev)
		}
		if err := Verify(e, s.PublicKey(), expectedPrev); err != nil {
			t.Fatalf("entry %d: Verify: %v", i, err)
		}
		// Compute hash of the signed entry; that's the next expectedPrev.
		signedCanonical, err := canonicalJSON(e)
		if err != nil {
			t.Fatalf("canonicalJSON: %v", err)
		}
		hash := hashHex(signedCanonical)
		expectedPrev = hash
	}

	// VerifyLog should also pass on the on-disk file.
	logPath := filepath.Join(dir, "attest-"+time.Now().UTC().Format("2006-01-02")+".jsonl")
	if err := VerifyLog(s.PublicKey(), logPath); err != nil {
		t.Fatalf("VerifyLog: %v", err)
	}

	// Tampering with the middle entry must break the chain.
	tampered := make([]Attestation, len(entries))
	copy(tampered, entries)
	tampered[1].Action = "tampered"
	tamperedPath := filepath.Join(dir, "tampered.jsonl")
	writeLog(t, tamperedPath, tampered)
	if err := VerifyLog(s.PublicKey(), tamperedPath); err == nil {
		t.Error("VerifyLog should fail on tampered chain")
	}
}

// TestAttest_LLMProvenanceRecorded verifies that LLM provenance (model,
// prompt hash, token counts, cost) is preserved through sign + verify.
func TestAttest_LLMProvenanceRecorded(t *testing.T) {
	s, err := NewSigner("", t.TempDir())
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	a := Attestation{
		Action: "fix_proposed",
		LLM: &LLMProvenance{
			Model:        "glm-4.5",
			PromptHash:   "abc123",
			InputTokens:  1500,
			OutputTokens: 800,
			CostUSD:      0.0125,
		},
		Evidence: []Evidence{
			{Kind: "sandbox", Source: "kaniko-build-42", Digest: "deadbeef"},
			{Kind: "log", Source: "pod/api-xyz", Payload: map[string]any{"restarts": 17}},
		},
	}
	if err := s.Sign(context.Background(), a); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	entries := readLog(t, s)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	got := entries[0]
	if got.LLM == nil {
		t.Fatal("expected LLM provenance to be recorded")
	}
	if got.LLM.Model != "glm-4.5" {
		t.Errorf("LLM.Model = %q, want glm-4.5", got.LLM.Model)
	}
	if got.LLM.PromptHash != "abc123" {
		t.Errorf("LLM.PromptHash = %q, want abc123", got.LLM.PromptHash)
	}
	if got.LLM.InputTokens != 1500 {
		t.Errorf("LLM.InputTokens = %d, want 1500", got.LLM.InputTokens)
	}
	if got.LLM.OutputTokens != 800 {
		t.Errorf("LLM.OutputTokens = %d, want 800", got.LLM.OutputTokens)
	}
	if got.LLM.CostUSD != 0.0125 {
		t.Errorf("LLM.CostUSD = %v, want 0.0125", got.LLM.CostUSD)
	}
	if len(got.Evidence) != 2 {
		t.Fatalf("expected 2 evidence entries, got %d", len(got.Evidence))
	}
	if got.Evidence[0].Kind != "sandbox" {
		t.Errorf("evidence[0].Kind = %q, want sandbox", got.Evidence[0].Kind)
	}

	// Signature must verify.
	if err := Verify(got, s.PublicKey(), ""); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

// TestAttest_NilSignerNoOp verifies that a nil *Signer is a no-op:
// Sign returns nil, PublicKey returns nil, PublicKeyHex returns "".
// This is the behavior callers rely on when AttestEnabled is false.
func TestAttest_NilSignerNoOp(t *testing.T) {
	var s *Signer
	if err := s.Sign(context.Background(), Attestation{Action: "test"}); err != nil {
		t.Errorf("nil Signer.Sign should return nil, got %v", err)
	}
	if pub := s.PublicKey(); pub != nil {
		t.Errorf("nil Signer.PublicKey should return nil, got %v", pub)
	}
	if hex := s.PublicKeyHex(); hex != "" {
		t.Errorf("nil Signer.PublicKeyHex should return empty, got %q", hex)
	}
	if h := s.PreviousHash(); h != "" {
		t.Errorf("nil Signer.PreviousHash should return empty, got %q", h)
	}
	if k := s.ExportPrivateKey(); k != nil {
		t.Errorf("nil Signer.ExportPrivateKey should return nil, got %v", k)
	}
}

// TestAttest_KeyPersistence verifies that a generated keypair is
// persisted to disk and can be reloaded by a second NewSigner call.
func TestAttest_KeyPersistence(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "signer.key")
	dir := t.TempDir()

	s1, err := NewSigner(keyPath, dir)
	if err != nil {
		t.Fatalf("NewSigner #1: %v", err)
	}
	pub1 := s1.PublicKeyHex()

	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("key file not created: %v", err)
	}

	s2, err := NewSigner(keyPath, dir)
	if err != nil {
		t.Fatalf("NewSigner #2: %v", err)
	}
	pub2 := s2.PublicKeyHex()

	if pub1 != pub2 {
		t.Errorf("reloaded key differs: %s vs %s", pub1, pub2)
	}
}

// TestAttest_ActorField verifies the Actor field defaults to "sentinel"
// and can be overridden (e.g. to "admin:alice" for human-initiated
// actions).
func TestAttest_ActorField(t *testing.T) {
	s, err := NewSigner("", t.TempDir())
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	// Default actor.
	if err := s.Sign(context.Background(), Attestation{Action: "auto_reverted"}); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Explicit admin actor.
	if err := s.Sign(context.Background(), Attestation{Action: "manual_approve", Actor: "admin:alice"}); err != nil {
		t.Fatalf("Sign: %v", err)
	}

	entries := readLog(t, s)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Actor != "sentinel" {
		t.Errorf("entry 0 Actor = %q, want sentinel", entries[0].Actor)
	}
	if entries[1].Actor != "admin:alice" {
		t.Errorf("entry 1 Actor = %q, want admin:alice", entries[1].Actor)
	}
}

// TestAttest_PublicKeyHexRoundTrip verifies that PublicKeyHex produces
// a hex string that can be decoded back to the original public key.
func TestAttest_PublicKeyHexRoundTrip(t *testing.T) {
	s, err := NewSigner("", t.TempDir())
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	hexStr := s.PublicKeyHex()
	if len(hexStr) != ed25519.PublicKeySize*2 {
		t.Fatalf("PublicKeyHex len = %d, want %d", len(hexStr), ed25519.PublicKeySize*2)
	}

	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	if !equalBytes(decoded, s.PublicKey()) {
		t.Error("decoded public key does not match original")
	}
}

// TestAttest_DeterministicCanonicalJSON verifies that the canonical JSON
// form is stable across calls (same input → same bytes). This is the
// property that lets signatures verify.
func TestAttest_DeterministicCanonicalJSON(t *testing.T) {
	a := Attestation{
		UUID:      "01923f8e-7c2a-7abc-9123-abcdef012345",
		Action:    "test",
		Actor:     "sentinel",
		Timestamp: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		Evidence:  []Evidence{{Kind: "log", Source: "pod/api"}},
	}

	b1, err := canonicalJSON(a)
	if err != nil {
		t.Fatalf("canonicalJSON #1: %v", err)
	}
	b2, err := canonicalJSON(a)
	if err != nil {
		t.Fatalf("canonicalJSON #2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("canonical JSON not deterministic:\n b1=%s\n b2=%s", b1, b2)
	}

	// Map key order in Evidence[0].Payload must be deterministic too.
	a.Evidence[0].Payload = map[string]any{"zebra": 1, "apple": 2, "mango": 3}
	b3, _ := canonicalJSON(a)
	b4, _ := canonicalJSON(a)
	if string(b3) != string(b4) {
		t.Errorf("canonical JSON not deterministic with map payload:\n b3=%s\n b4=%s", b3, b4)
	}
}

// TestAttest_CryptoRandUsedForUUID verifies that two UUIDs generated in
// the same millisecond have different random bits (i.e. crypto/rand is
// actually being used, not a constant or time-derived value).
func TestAttest_CryptoRandUsedForUUID(t *testing.T) {
	// Generate 100 UUIDs as fast as possible (likely in the same ms).
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		u := newUUIDv7()
		if seen[u] {
			t.Fatalf("duplicate UUID generated: %s", u)
		}
		seen[u] = true
	}
}

// --- helpers ---

func readLog(t *testing.T, s *Signer) []Attestation {
	t.Helper()
	dir := s.logDir
	if dir == "" {
		t.Fatal("logDir is empty")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []Attestation
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if line == "" {
				continue
			}
			var a Attestation
			if err := json.Unmarshal([]byte(line), &a); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			out = append(out, a)
		}
	}
	return out
}

func writeLog(t *testing.T, path string, entries []Attestation) {
	t.Helper()
	var buf strings.Builder
	for _, e := range entries {
		line, _ := json.Marshal(e)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
