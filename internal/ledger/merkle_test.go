package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
)

// The tree is RFC 6962's: the empty tree and an empty leaf hash as
// Certificate Transparency's test vectors say.
func TestTheTreeIsCertificateTransparencys(t *testing.T) {
	if got := hex.EncodeToString(treeHash(nil)); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty tree = %s", got)
	}
	if got := hex.EncodeToString(leafHash(nil)); got != "6e340b9cffb37a989ca544e6bb780a2c78901d3fb33738768511a30617afa01d" {
		t.Fatalf("empty leaf = %s", got)
	}
	var leaves [][]byte
	for _, in := range []string{"", "00", "10", "2021", "3031", "40414243", "5051525354555657", "606162636465666768696a6b6c6d6e6f"} {
		b, _ := hex.DecodeString(in)
		leaves = append(leaves, leafHash(b))
	}
	if got := hex.EncodeToString(treeHash(leaves)); got != "5dc9da79a70659a9ad559cb701ded9a2ab9d823aad2f4960cfe370eff4604328" {
		t.Fatalf("the eight leaves of RFC 6962's tests hash to %s", got)
	}
}

// Every event of every tree up to 33 events has a proof that checks, and
// every tree is shown to begin every larger one; a changed hash fails both.
func TestProofsCheckAndTamperingFails(t *testing.T) {
	var leaves [][]byte
	for i := range 33 {
		sum := sha256.Sum256([]byte(fmt.Sprint(i)))
		leaves = append(leaves, leafHash(sum[:]))
	}
	for n := 1; n <= len(leaves); n++ {
		tree := leaves[:n]
		root := hex.EncodeToString(treeHash(tree))
		for m := range n {
			sum := sha256.Sum256([]byte(fmt.Sprint(m)))
			p := InclusionProof{Seq: int64(m + 1), Size: int64(n), EventHash: hex.EncodeToString(sum[:]), Path: encode(auditPath(m, tree)), Root: root}
			if err := p.Check(); err != nil {
				t.Fatalf("event %d of %d: %v", m+1, n, err)
			}
			p.EventHash = hex.EncodeToString(sha256.New().Sum([]byte("other"))[:32])
			if err := p.Check(); !errors.Is(err, ErrProof) {
				t.Fatalf("event %d of %d with another hash: %v", m+1, n, err)
			}
		}
		for m := 1; m <= n; m++ {
			var path [][]byte
			if m < n {
				path = subproof(m, tree, true)
			}
			p := ConsistencyProof{From: int64(m), To: int64(n), Path: encode(path), OldRoot: hex.EncodeToString(treeHash(tree[:m])), NewRoot: root}
			if err := p.Check(); err != nil {
				t.Fatalf("%d in %d: %v", m, n, err)
			}
			if m < n {
				p.OldRoot = hex.EncodeToString(treeHash(leaves[1 : m+1]))
				if err := p.Check(); !errors.Is(err, ErrProof) {
					t.Fatalf("%d in %d with another beginning: %v", m, n, err)
				}
			}
		}
	}
}

// The Ledger proves its own events, and its tree at an earlier size.
func TestTheLedgerProvesItsEvents(t *testing.T) {
	ctx := context.Background()
	l, _ := openTemp(t)
	for i := range 9 {
		if _, err := l.Append(ctx, Draft{Type: "sentinel.test", Source: "/test", Subject: fmt.Sprint(i), Data: map[string]int{"i": i}}); err != nil {
			t.Fatal(err)
		}
	}
	size, root, err := l.Root(ctx, 0)
	if err != nil || size != 9 {
		t.Fatalf("root of %d: %s, %v", size, root, err)
	}
	p, err := l.Prove(ctx, 4, 0)
	if err != nil || p.Root != root || p.Check() != nil {
		t.Fatalf("proof %+v: %v, %v", p, err, p.Check())
	}
	c, err := l.Consistency(ctx, 5, 0)
	if err != nil || c.NewRoot != root || c.Check() != nil {
		t.Fatalf("consistency %+v: %v, %v", c, err, c.Check())
	}
	if _, _, err := l.Root(ctx, 10); err == nil {
		t.Fatal("a root of more events than the ledger holds")
	}
}
