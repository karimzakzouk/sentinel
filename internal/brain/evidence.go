package brain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/karimzakzouk/sentinel/internal/ledger"
	"github.com/karimzakzouk/sentinel/internal/model"
	"github.com/karimzakzouk/sentinel/internal/watcher"
)

const (
	// evidenceTTL is how long log evidence waits for an incident to join.
	evidenceTTL = 30 * time.Minute
	// evidenceLead is how long before an incident began evidence may have
	// been collected and still belong to it.
	evidenceLead = 10 * time.Minute
	// maxPendingEvidence bounds the evidence kept per workload.
	maxPendingEvidence = 20
)

type pendingEvidence struct {
	ev         watcher.LogEvidence
	attachedTo model.ID
}

// OnEvidence keeps log evidence for the workload's incident: the open one
// now, or one that opens within the TTL. Evidence that never joins an
// incident never reaches the Ledger.
func (b *Brain) OnEvidence(ctx context.Context, ev watcher.LogEvidence) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.evidence == nil {
		b.evidence = map[string][]*pendingEvidence{}
	}
	key := ev.Workload.String()
	pending := append(b.liveEvidence(key), &pendingEvidence{ev: ev})
	if len(pending) > maxPendingEvidence {
		pending = pending[len(pending)-maxPendingEvidence:]
	}
	b.evidence[key] = pending
	inc := b.route[key]
	if inc == nil {
		return nil
	}
	drafts := b.evidenceDrafts(inc, key)
	if len(drafts) == 0 {
		return nil
	}
	drafts = append(drafts, ledger.RecordDraft(source, model.KindIncident, "updated", inc.rec.ID, inc.rec.ID, inc.rec))
	_, err := b.Ledger.Append(ctx, drafts...)
	return err
}

// liveEvidence returns the workload's evidence that has not expired.
// Callers hold b.mu.
func (b *Brain) liveEvidence(key string) []*pendingEvidence {
	now := b.now()
	var live []*pendingEvidence
	for _, p := range b.evidence[key] {
		if now.Sub(p.ev.CollectedAt) < evidenceTTL {
			live = append(live, p)
		}
	}
	return live
}

// evidenceDrafts records the workload's evidence that belongs to the
// incident and has not joined it yet. Uninformative samples and repeats of
// content the incident already holds (a crash loop's every crash prints the
// same lines) are left out. Callers hold b.mu.
func (b *Brain) evidenceDrafts(inc *incident, key string) []ledger.Draft {
	var drafts []ledger.Draft
	for _, p := range b.evidence[key] {
		if p.attachedTo == inc.rec.ID || p.ev.CollectedAt.Before(inc.rec.OpenedAt.Add(-evidenceLead)) || !informative(p.ev) {
			continue
		}
		p.attachedTo = inc.rec.ID
		digest := contentDigest(p.ev)
		if inc.evidenceDigests[digest] {
			continue
		}
		if inc.evidenceDigests == nil {
			inc.evidenceDigests = map[string]bool{}
		}
		inc.evidenceDigests[digest] = true
		e := evidenceRecord(inc.rec.ID, p.ev)
		inc.rec.EvidenceIDs = append(inc.rec.EvidenceIDs, e.ID)
		drafts = append(drafts, ledger.RecordDraft(source, model.KindEvidence, "attached", e.ID, inc.rec.ID, e))
		b.Log.Info("evidence attached", "incident", inc.rec.ID, "workload", key, "source", e.Source, "pod", p.ev.Pod, "summary", e.Summary)
	}
	return drafts
}

// informative reports evidence worth recording: the logs of a crash or an
// unready pod, or a rollout sample that holds errors.
func informative(ev watcher.LogEvidence) bool {
	if ev.Summary.Lines == 0 {
		return false
	}
	if ev.Reason == "crash" || ev.Reason == "not-ready" || len(ev.Summary.Exceptions) > 0 {
		return true
	}
	for _, t := range ev.Summary.Templates {
		if t.Error {
			return true
		}
	}
	return false
}

// contentDigest identifies what a log says, whichever pod said it.
func contentDigest(ev watcher.LogEvidence) string {
	type tpl struct {
		T string
		N int
	}
	content := struct {
		Container, Reason string
		Templates         []tpl
		Exceptions        []tpl
	}{Container: ev.Container, Reason: ev.Reason}
	for _, t := range ev.Summary.Templates {
		content.Templates = append(content.Templates, tpl{t.Template, t.Count})
	}
	for _, e := range ev.Summary.Exceptions {
		content.Exceptions = append(content.Exceptions, tpl{e.Fingerprint, e.Count})
	}
	b, _ := json.Marshal(content)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// pruneEvidence drops expired evidence. Callers hold b.mu.
func (b *Brain) pruneEvidence() {
	for key := range b.evidence {
		if live := b.liveEvidence(key); len(live) > 0 {
			b.evidence[key] = live
		} else {
			delete(b.evidence, key)
		}
	}
}

func evidenceRecord(incident model.ID, ev watcher.LogEvidence) model.Evidence {
	data, _ := json.Marshal(ev)
	src := "logs/current"
	if ev.Previous {
		src = "logs/previous"
	}
	return model.Evidence{
		ID: model.NewID(), IncidentID: incident, Source: src,
		Object: model.ObjectRef{Kind: "Pod", Namespace: ev.Workload.Namespace, Name: ev.Pod},
		// The workload wrote this text; it is evidence, never instruction.
		Trust: model.TrustUntrusted, SHA256: ev.Summary.SHA256, Summary: describeLogs(ev), Data: data,
		CollectedAt: ev.CollectedAt, Redacted: true,
	}
}

// describeLogs is a one-line account of a log's errors, from its redacted
// templates and exceptions.
func describeLogs(ev watcher.LogEvidence) string {
	parts := []string{fmt.Sprintf("%s logs of %s/%s: %d lines", ev.Reason, ev.Pod, ev.Container, ev.Summary.Lines)}
	for _, e := range ev.Summary.Exceptions[:min(len(ev.Summary.Exceptions), 2)] {
		parts = append(parts, fmt.Sprintf("%s ×%d", strings.Join(e.Types, " > "), e.Count))
	}
	shown := 0
	for _, t := range ev.Summary.Templates {
		if t.Error && shown < 2 {
			parts = append(parts, fmt.Sprintf("%q ×%d", t.Template, t.Count))
			shown++
		}
	}
	return strings.Join(parts, "; ")
}
