package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// The Ledger's events, by their hashes in sequence order, are the leaves of
// a Merkle tree as Certificate Transparency builds one (RFC 6962): a leaf
// hashes 0x00 and the event's hash, a node 0x01 and its two children. A
// root published at some size lets anyone check, with a proof of a few
// hashes, that an event is in the log, and that the log at a later size
// still begins with every event it had.

// ErrProof reports a proof that does not lead to the root it was checked
// against.
var ErrProof = errors.New("merkle proof does not match the root")

// InclusionProof shows that the event at Seq is in the tree of Size events
// with Root.
type InclusionProof struct {
	Seq       int64    `json:"sequence"`
	Size      int64    `json:"size"`
	EventHash string   `json:"eventHash"`
	Path      []string `json:"path"`
	Root      string   `json:"root"`
}

// ConsistencyProof shows that the tree of From events with OldRoot is the
// beginning of the tree of To events with NewRoot.
type ConsistencyProof struct {
	From    int64    `json:"from"`
	To      int64    `json:"to"`
	Path    []string `json:"path"`
	OldRoot string   `json:"oldRoot"`
	NewRoot string   `json:"newRoot"`
}

// Root returns the Merkle root of the first size events; size 0 means
// every event.
func (l *Ledger) Root(ctx context.Context, size int64) (int64, string, error) {
	leaves, err := l.leaves(ctx, size)
	if err != nil {
		return 0, "", err
	}
	return int64(len(leaves)), hex.EncodeToString(treeHash(leaves)), nil
}

// Prove returns the proof that event seq is in the tree of the first size
// events; size 0 means every event.
func (l *Ledger) Prove(ctx context.Context, seq, size int64) (InclusionProof, error) {
	leaves, err := l.leaves(ctx, size)
	if err != nil {
		return InclusionProof{}, err
	}
	if seq < 1 || seq > int64(len(leaves)) {
		return InclusionProof{}, fmt.Errorf("event %d is not among the %d events", seq, len(leaves))
	}
	hashes, err := l.queryHashes(ctx, seq, seq)
	if err != nil {
		return InclusionProof{}, err
	}
	return InclusionProof{Seq: seq, Size: int64(len(leaves)), EventHash: hashes[0], Path: encode(auditPath(int(seq-1), leaves)),
		Root: hex.EncodeToString(treeHash(leaves))}, nil
}

// Consistency returns the proof that the tree of the first from events is
// the beginning of the tree of the first to; to 0 means every event.
func (l *Ledger) Consistency(ctx context.Context, from, to int64) (ConsistencyProof, error) {
	leaves, err := l.leaves(ctx, to)
	if err != nil {
		return ConsistencyProof{}, err
	}
	if from < 1 || from > int64(len(leaves)) {
		return ConsistencyProof{}, fmt.Errorf("a tree of %d events is not a beginning of one of %d", from, len(leaves))
	}
	var path [][]byte
	if from < int64(len(leaves)) {
		path = subproof(int(from), leaves, true)
	}
	return ConsistencyProof{From: from, To: int64(len(leaves)), Path: encode(path),
		OldRoot: hex.EncodeToString(treeHash(leaves[:from])), NewRoot: hex.EncodeToString(treeHash(leaves))}, nil
}

// leaves are the leaf hashes of the first size events, every event for 0.
func (l *Ledger) leaves(ctx context.Context, size int64) ([][]byte, error) {
	last, _, err := l.Head(ctx)
	if err != nil {
		return nil, err
	}
	if size == 0 || size > last {
		if size > last {
			return nil, fmt.Errorf("the ledger holds %d events, not %d", last, size)
		}
		size = last
	}
	hashes, err := l.queryHashes(ctx, 1, size)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, len(hashes))
	for i, h := range hashes {
		b, err := hex.DecodeString(h)
		if err != nil {
			return nil, fmt.Errorf("event %d has a malformed hash: %w", i+1, err)
		}
		out[i] = leafHash(b)
	}
	return out, nil
}

func (l *Ledger) queryHashes(ctx context.Context, from, to int64) ([]string, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT hash FROM events WHERE seq >= ? AND seq <= ? ORDER BY seq`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query hashes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func leafHash(eventHash []byte) []byte {
	h := sha256.New()
	h.Write([]byte{0})
	h.Write(eventHash)
	return h.Sum(nil)
}

func nodeHash(left, right []byte) []byte {
	h := sha256.New()
	h.Write([]byte{1})
	h.Write(left)
	h.Write(right)
	return h.Sum(nil)
}

// split is the largest power of two smaller than n, for n > 1.
func split(n int) int {
	k := 1
	for k<<1 < n {
		k <<= 1
	}
	return k
}

// treeHash is RFC 6962's Merkle Tree Hash of leaf hashes.
func treeHash(leaves [][]byte) []byte {
	switch len(leaves) {
	case 0:
		sum := sha256.Sum256(nil)
		return sum[:]
	case 1:
		return leaves[0]
	}
	k := split(len(leaves))
	return nodeHash(treeHash(leaves[:k]), treeHash(leaves[k:]))
}

// auditPath is RFC 6962's PATH(m, D[n]).
func auditPath(m int, leaves [][]byte) [][]byte {
	if len(leaves) <= 1 {
		return nil
	}
	k := split(len(leaves))
	if m < k {
		return append(auditPath(m, leaves[:k]), treeHash(leaves[k:]))
	}
	return append(auditPath(m-k, leaves[k:]), treeHash(leaves[:k]))
}

// subproof is RFC 6962's SUBPROOF(m, D[n], b).
func subproof(m int, leaves [][]byte, whole bool) [][]byte {
	n := len(leaves)
	if m == n {
		if whole {
			return nil
		}
		return [][]byte{treeHash(leaves)}
	}
	k := split(n)
	if m <= k {
		return append(subproof(m, leaves[:k], whole), treeHash(leaves[k:]))
	}
	return append(subproof(m-k, leaves[k:], false), treeHash(leaves[:k]))
}

// Check verifies an inclusion proof against its root, as RFC 9162
// describes, so a proof is checked without the Ledger.
func (p InclusionProof) Check() error {
	event, path, root, err := decode(p.EventHash, p.Path, p.Root)
	if err != nil {
		return err
	}
	if p.Seq < 1 || p.Seq > p.Size {
		return fmt.Errorf("%w: event %d is outside a tree of %d", ErrProof, p.Seq, p.Size)
	}
	fn, sn, r := p.Seq-1, p.Size-1, leafHash(event)
	for _, c := range path {
		if sn == 0 {
			return fmt.Errorf("%w: the path is too long", ErrProof)
		}
		if fn&1 == 1 || fn == sn {
			r = nodeHash(c, r)
			for fn&1 == 0 && fn != 0 {
				fn, sn = fn>>1, sn>>1
			}
		} else {
			r = nodeHash(r, c)
		}
		fn, sn = fn>>1, sn>>1
	}
	if sn != 0 || !bytes.Equal(r, root) {
		return ErrProof
	}
	return nil
}

// Check verifies a consistency proof against its two roots, as RFC 9162
// describes.
func (p ConsistencyProof) Check() error {
	old, path, root, err := decode(p.OldRoot, p.Path, p.NewRoot)
	if err != nil {
		return err
	}
	switch {
	case p.From < 1 || p.From > p.To:
		return fmt.Errorf("%w: a tree of %d is not a beginning of one of %d", ErrProof, p.From, p.To)
	case p.From == p.To:
		if len(path) != 0 || !bytes.Equal(old, root) {
			return ErrProof
		}
		return nil
	case len(path) == 0:
		return fmt.Errorf("%w: the path is empty", ErrProof)
	}
	if p.From&(p.From-1) == 0 {
		path = append([][]byte{old}, path...)
	}
	fn, sn := p.From-1, p.To-1
	for fn&1 == 1 {
		fn, sn = fn>>1, sn>>1
	}
	fr, sr := path[0], path[0]
	for _, c := range path[1:] {
		if sn == 0 {
			return fmt.Errorf("%w: the path is too long", ErrProof)
		}
		if fn&1 == 1 || fn == sn {
			fr, sr = nodeHash(c, fr), nodeHash(c, sr)
			for fn&1 == 0 && fn != 0 {
				fn, sn = fn>>1, sn>>1
			}
		} else {
			sr = nodeHash(sr, c)
		}
		fn, sn = fn>>1, sn>>1
	}
	if sn != 0 || !bytes.Equal(fr, old) || !bytes.Equal(sr, root) {
		return ErrProof
	}
	return nil
}

func encode(hashes [][]byte) []string {
	out := make([]string, len(hashes))
	for i, h := range hashes {
		out[i] = hex.EncodeToString(h)
	}
	return out
}

func decode(first string, path []string, root string) ([]byte, [][]byte, []byte, error) {
	f, err := hex.DecodeString(first)
	if err != nil {
		return nil, nil, nil, err
	}
	r, err := hex.DecodeString(root)
	if err != nil {
		return nil, nil, nil, err
	}
	p := make([][]byte, len(path))
	for i, s := range path {
		if p[i], err = hex.DecodeString(s); err != nil {
			return nil, nil, nil, err
		}
	}
	return f, p, r, nil
}
