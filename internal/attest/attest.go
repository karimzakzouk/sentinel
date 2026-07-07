// Package attest implements the signed audit trail for Sentinel's
// autonomous actions.
//
// WHY THIS EXISTS
//
// The EU AI Act enforcement begins August 2026 and explicitly covers
// autonomous AI agents. The IETF published "Agent Audit Trail: A Standard
// Logging Format for Autonomous AI Systems" in March 2026 (datatracker.ietf.org).
// SOC 2, ISO 27001, and enterprise procurement teams now require verifiable
// audit trails for any AI that takes production actions.
//
// Every autonomous action Sentinel takes must emit a signed attestation:
//   - What was observed (the incident signals)
//   - What the LLM proposed (with prompt hash + model + token count + cost)
//   - What evidence was gathered (sandbox results, metric snapshots)
//   - What was verified (CI status, test results, semantic diff)
//   - What was merged (PR number, commit SHA, merge commit)
//   - What happened after (post-merge regression check result)
//
// Each attestation is signed with the Sentinel instance's Ed25519 keypair.
// The signature can be verified by anyone with the public key — including
// external auditors, customers' security teams, and insurance underwriters.
//
// STORAGE
//
// Attestations are stored in the embedded SQLite DB (internal/feedback) and
// also written as JSONL files to a configurable on-disk directory for
// long-term tamper-evident retention (each attestation references the SHA256
// of the previous one, forming a hash chain).
package attest

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Attestation is a single signed record of an autonomous action.
//
// The schema intentionally follows the IETF "Agent Audit Trail" draft
// so external tooling can consume it without Sentinel-specific parsing.
type Attestation struct {
	ID          string    `json:"id"`
	IncidentID  string    `json:"incident_id"`
	Action      string    `json:"action"`
	Timestamp   time.Time `json:"timestamp"`

	Reason   string                 `json:"reason"`
	Outcome  bool                   `json:"outcome"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`

	LLM *LLMProvenance `json:"llm,omitempty"`

	Evidence Evidence `json:"evidence"`

	PrevHash  string `json:"prev_hash"`
	Signature string `json:"signature"`
}

// LLMProvenance records exactly which model produced this decision.
type LLMProvenance struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	PromptHash   string  `json:"prompt_hash"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Temperature  float64 `json:"temperature"`
	Seed         int64   `json:"seed"`
}

// Evidence bundles the common observation/action payloads.
type Evidence struct {
	AlertSource      string                 `json:"alert_source,omitempty"`
	AlertPayload     map[string]interface{} `json:"alert_payload,omitempty"`
	RootCause        string                 `json:"root_cause,omitempty"`
	Confidence       float64                `json:"confidence,omitempty"`
	SimilarIncidents []string               `json:"similar_incidents,omitempty"`
	PRNumber         int                    `json:"pr_number,omitempty"`
	PRURL            string                 `json:"pr_url,omitempty"`
	DiffStats        *DiffStats             `json:"diff_stats,omitempty"`
	SandboxResult    string                 `json:"sandbox_result,omitempty"`
	SandboxLogs      string                 `json:"sandbox_logs,omitempty"`
	PreMerge         interface{}            `json:"pre_merge,omitempty"`
	PostMerge        interface{}            `json:"post_merge,omitempty"`
	RevertPR         int                    `json:"revert_pr,omitempty"`
	Extras           map[string]interface{} `json:"extras,omitempty"`
}

// DiffStats summarizes a PR diff.
type DiffStats struct {
	FilesChanged int `json:"files_changed"`
	Insertions   int `json:"insertions"`
	Deletions    int `json:"deletions"`
}

// Signer signs attestations and persists them to disk (+ eventually SQLite).
type Signer struct {
	mu         sync.Mutex
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	instanceID string
	prevHash   string
	logDir     string
	logger     *slog.Logger
}

// NewSigner creates a Signer. If privateKey is nil, a new keypair is generated.
func NewSigner(privateKey ed25519.PrivateKey, instanceID, logDir string, logger *slog.Logger) (*Signer, error) {
	if len(privateKey) == 0 {
		var err error
		_, privateKey, err = ed25519.GenerateKey(nil)
		if err != nil {
			return nil, fmt.Errorf("attest: generate keypair: %w", err)
		}
	}
	pub := privateKey.Public().(ed25519.PublicKey)

	if logDir != "" {
		if err := os.MkdirAll(logDir, 0o700); err != nil {
			return nil, fmt.Errorf("attest: create log dir: %w", err)
		}
	}

	return &Signer{
		privateKey: privateKey,
		publicKey:  pub,
		instanceID: instanceID,
		logDir:     logDir,
		logger:     logger,
	}, nil
}

// PublicKey returns the Ed25519 public key for this Sentinel instance.
func (s *Signer) PublicKey() ed25519.PublicKey { return s.publicKey }

// PublicKeyHex returns the public key as a hex string for easy export.
func (s *Signer) PublicKeyHex() string { return hex.EncodeToString(s.publicKey) }

// Sign signs and persists an attestation.
func (s *Signer) Sign(ctx context.Context, a Attestation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if a.Timestamp.IsZero() {
		a.Timestamp = time.Now().UTC()
	}
	if a.ID == "" {
		a.ID = newUUIDv7()
	}
	a.PrevHash = s.prevHash

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
			s.logger.Error("attest: failed to open log file", "path", path, "err", err)
		} else {
			line, _ := json.Marshal(a)
			if _, err := f.Write(append(line, '\n')); err != nil {
				s.logger.Error("attest: failed to write log line", "err", err)
			}
			f.Close()
		}
	}

	s.logger.Info("attest: signed",
		"id", a.ID,
		"incident_id", a.IncidentID,
		"action", a.Action,
		"outcome", a.Outcome,
	)
	return nil
}

// Verify checks that an attestation was signed by the given public key and
// that its hash-chain prev_hash matches the supplied previous hash.
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

	if expectedPrevHash != "" && a.PrevHash != expectedPrevHash {
		return fmt.Errorf("attest: hash chain broken — expected prev_hash=%s got %s", expectedPrevHash, a.PrevHash)
	}
	return nil
}

// canonicalJSON serializes a struct as JSON with sorted keys and no whitespace.
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

// newUUIDv7 returns a time-ordered UUIDv7.
// TODO: replace with google/uuid's NewV7 once we add it to go.mod.
func newUUIDv7() string {
	now := time.Now().UTC()
	var b [16]byte
	ms := uint64(now.UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = 0x70 | 0x0f
	b[7] = 0x00
	for i := 8; i < 16; i++ {
		b[i] = byte(now.UnixNano() >> uint(i*4))
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
