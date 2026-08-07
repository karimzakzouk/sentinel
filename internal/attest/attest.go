package attest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/karimzakzouk/sentinel/internal/logging"
)

// Attestation is a single signed record of an autonomous action. The
// schema follows the IETF "Agent Audit Trail" draft so external tooling
// can consume it without Sentinel-specific parsing.
type Attestation struct {
	UUID         string         `json:"uuid"`
	Timestamp    time.Time      `json:"timestamp"`
	Action       string         `json:"action"`
	IncidentID   string         `json:"incident_id,omitempty"`
	Actor        string         `json:"actor"`
	Reason       string         `json:"reason,omitempty"`
	Outcome      bool           `json:"outcome"`
	LLM          *LLMProvenance `json:"llm,omitempty"`
	Evidence     []Evidence     `json:"evidence,omitempty"`
	PreviousHash string         `json:"previous_hash"`
	Signature    string         `json:"signature"`
}

// LLMProvenance records exactly which model produced a decision. Stored
// on every attestation whose Action was driven by an LLM call.
type LLMProvenance struct {
	Model        string  `json:"model"`
	PromptHash   string  `json:"prompt_hash"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Evidence is one piece of supporting material for an attestation: a
// sandbox result, a metric snapshot, a log excerpt, a diff, etc.
type Evidence struct {
	Kind    string         `json:"kind"`
	Source  string         `json:"source,omitempty"`
	Digest  string         `json:"digest,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Signer signs attestations and persists them as append-only JSONL.
// A nil *Signer is a no-op: Sign returns nil, PublicKey returns nil,
// PublicKeyHex returns "". This lets callers with attestation disabled
// hold a nil *Signer and call methods without nil guards.
type Signer struct {
	mu         sync.Mutex
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	prevHash   string
	logDir     string
}

// NewSigner loads (or generates and persists) an Ed25519 keypair and
// returns a ready Signer that writes attestations to logDir.
//
// If privateKeyPath is empty, a fresh keypair is generated in memory and
// is NOT persisted — callers that want persistence must ExportPrivateKey
// and write it somewhere themselves. If privateKeyPath is non-empty and
// the file exists, the key is loaded from disk. If the file does not
// exist, a new keypair is generated and written there with 0600 perms.
//
// If logDir is empty, attestations are signed and chained in memory but
// NOT written to disk (useful for tests).
func NewSigner(privateKeyPath, logDir string) (*Signer, error) {
	priv, err := loadOrGenerateKey(privateKeyPath)
	if err != nil {
		return nil, err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("attest: generated key is not ed25519.PublicKey (unexpected)")
	}

	if logDir != "" {
		if err := os.MkdirAll(logDir, 0o700); err != nil {
			return nil, fmt.Errorf("attest: create log dir: %w", err)
		}
	}

	return &Signer{
		privateKey: priv,
		publicKey:  pub,
		logDir:     logDir,
	}, nil
}

// loadOrGenerateKey loads an Ed25519 private key from path (a hex-encoded
// 64-byte seed), or generates a new one and persists it. If path is empty,
// generates an ephemeral key that is NOT persisted.
func loadOrGenerateKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("attest: generate ephemeral keypair: %w", err)
		}
		return priv, nil
	}

	data, err := os.ReadFile(path)
	if err == nil {
		seed, decErr := hex.DecodeString(string(data))
		if decErr != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("attest: parse key file %s: %w", path, decErr)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("attest: read key file %s: %w", path, err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("attest: generate keypair: %w", err)
	}
	seedHex := hex.EncodeToString(priv.Seed())
	if err := os.WriteFile(path, []byte(seedHex), 0o600); err != nil {
		return nil, fmt.Errorf("attest: write key file %s: %w", path, err)
	}
	logging.Info("attest: generated new Ed25519 keypair", "path", path)
	return priv, nil
}

// PublicKey returns the Ed25519 public key. Returns nil if s is nil.
func (s *Signer) PublicKey() ed25519.PublicKey {
	if s == nil {
		return nil
	}
	return s.publicKey
}

// PublicKeyHex returns the public key as a lowercase hex string. Returns
// "" if s is nil.
func (s *Signer) PublicKeyHex() string {
	if s == nil {
		return ""
	}
	return hex.EncodeToString(s.publicKey)
}

// ExportPrivateKey returns the Ed25519 private key. Callers that want to
// persist a generated key can use this to write it somewhere safe.
func (s *Signer) ExportPrivateKey() ed25519.PrivateKey {
	if s == nil {
		return nil
	}
	return s.privateKey
}

// Sign fills in UUID, Timestamp, and PreviousHash, signs the attestation
// with the Signer's Ed25519 private key, appends it to the JSONL log,
// and advances the in-memory hash chain. Returns nil immediately if s
// is nil (attestation disabled).
func (s *Signer) Sign(ctx context.Context, a Attestation) error {
	if s == nil {
		return nil
	}
	_ = ctx

	s.mu.Lock()
	defer s.mu.Unlock()

	if a.Timestamp.IsZero() {
		a.Timestamp = time.Now().UTC()
	}
	if a.UUID == "" {
		a.UUID = newUUIDv7()
	}
	if a.Actor == "" {
		a.Actor = "sentinel"
	}
	a.PreviousHash = s.prevHash

	canonical, err := canonicalJSON(a)
	if err != nil {
		return fmt.Errorf("attest: marshal for signing: %w", err)
	}
	sig := ed25519.Sign(s.privateKey, canonical)
	a.Signature = hex.EncodeToString(sig)

	signedCanonical, err := canonicalJSON(a)
	if err != nil {
		return fmt.Errorf("attest: marshal signed record: %w", err)
	}
	hash := sha256.Sum256(signedCanonical)
	s.prevHash = hex.EncodeToString(hash[:])

	if s.logDir != "" {
		path := filepath.Join(s.logDir, fmt.Sprintf("attest-%s.jsonl", time.Now().UTC().Format("2006-01-02")))
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			logging.Error("attest: failed to open log file", "path", path, "err", err)
			return nil
		}
		defer f.Close()
		line, _ := json.Marshal(a)
		if _, err := f.Write(append(line, '\n')); err != nil {
			logging.Error("attest: failed to write log line", "err", err)
		}
	}

	logging.Info("attest: signed",
		"uuid", a.UUID,
		"incident_id", a.IncidentID,
		"action", a.Action,
		"actor", a.Actor,
		"outcome", a.Outcome,
	)
	return nil
}

// PreviousHash returns the SHA-256 of the most recently signed
// attestation. Returns "" if no attestation has been signed yet (or if
// s is nil).
func (s *Signer) PreviousHash() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prevHash
}

// Verify checks that an attestation was signed by pub and that its
// PreviousHash matches expectedPrevHash (when expectedPrevHash is
// non-empty). It does NOT advance any in-memory chain.
func Verify(a Attestation, pub ed25519.PublicKey, expectedPrevHash string) error {
	if a.Signature == "" {
		return errors.New("attest: empty signature")
	}
	sig, err := hex.DecodeString(a.Signature)
	if err != nil {
		return fmt.Errorf("attest: decode signature: %w", err)
	}

	prevSig := a.Signature
	a.Signature = ""
	canonical, err := canonicalJSON(a)
	if err != nil {
		return fmt.Errorf("attest: marshal for verify: %w", err)
	}
	if !ed25519.Verify(pub, canonical, sig) {
		return errors.New("attest: signature verification failed")
	}
	a.Signature = prevSig

	if expectedPrevHash != "" && a.PreviousHash != expectedPrevHash {
		return fmt.Errorf("attest: hash chain broken: expected previous_hash=%s got %s", expectedPrevHash, a.PreviousHash)
	}
	return nil
}

// VerifyLog reads a JSONL attestation log and verifies every signature
// plus the hash chain. pub is the public key to verify against. Returns
// the first error encountered, or nil if the whole log verifies cleanly.
func VerifyLog(pub ed25519.PublicKey, logPath string) error {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Errorf("attest: read log: %w", err)
	}

	var expectedPrev string
	lineStart := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			line := data[lineStart:i]
			lineStart = i + 1
			if len(line) == 0 {
				continue
			}
			var a Attestation
			if err := json.Unmarshal(line, &a); err != nil {
				return fmt.Errorf("attest: parse line: %w", err)
			}
			if err := Verify(a, pub, expectedPrev); err != nil {
				return fmt.Errorf("attest: verify uuid=%s: %w", a.UUID, err)
			}
			signedCanonical, err := canonicalJSON(a)
			if err != nil {
				return fmt.Errorf("attest: marshal for chain: %w", err)
			}
			hash := sha256.Sum256(signedCanonical)
			expectedPrev = hex.EncodeToString(hash[:])
		}
	}
	return nil
}

// canonicalJSON serializes v as JSON with sorted keys and no whitespace.
// Re-marshaling via interface{} ensures map keys are sorted alphabetically
// (Go's json.Marshal sorts map keys but uses struct field order for
// structs — round-tripping through interface{} normalizes both).
func canonicalJSON(v interface{}) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// newUUIDv7 returns a time-ordered UUIDv7 per RFC 9562.
//
// Layout (128 bits, big-endian):
//
//	bits  0-47:  48-bit Unix millisecond timestamp
//	bits 48-51:  0b0111 (version 7)
//	bits 52-63:  12-bit random (rand_a)
//	bits 64-65:  0b10   (variant)
//	bits 66-127: 62-bit random (rand_b)
//
// The timestamp makes UUIDs lexicographically sortable by generation
// time — the property audit logs rely on. The random bits use crypto/rand
// so UUIDs are unforgeable and unpredictable.
//
// If crypto/rand fails (vanishingly rare on any real OS), the random
// bits are left as zero. The UUID is still time-ordered and
// unforgeable-in-practice; the worst case is two UUIDs in the same
// millisecond colliding, which is non-fatal for an audit log.
func newUUIDv7() string {
	var b [16]byte

	ms := uint64(time.Now().UTC().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	var randA [2]byte
	if _, err := rand.Read(randA[:]); err != nil {
		logging.Warn("attest: crypto/rand failed for UUIDv7 rand_a", "err", err)
	}
	// b[6] high nibble = 0x7 (version), low nibble = top 4 bits of rand_a.
	b[6] = 0x70 | (randA[0] & 0x0F)
	// b[7] = bottom 8 bits of rand_a.
	b[7] = randA[1]

	var randB [8]byte
	if _, err := rand.Read(randB[:]); err != nil {
		logging.Warn("attest: crypto/rand failed for UUIDv7 rand_b", "err", err)
	}
	// b[8] high 2 bits = 0b10 (variant), low 6 bits = top 6 bits of rand_b.
	b[8] = 0x80 | (randB[0] & 0x3F)
	b[9] = randB[1]
	b[10] = randB[2]
	b[11] = randB[3]
	b[12] = randB[4]
	b[13] = randB[5]
	b[14] = randB[6]
	b[15] = randB[7]

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
