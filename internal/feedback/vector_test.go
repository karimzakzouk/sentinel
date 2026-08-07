package feedback

import (
        "context"
        "testing"

        "github.com/karimzakzouk/sentinel/internal/emb"
)

// TestVectorStore_Search_RealScores verifies that VectorStore.Search
// returns REAL cosine similarity scores (not fabricated ones). The
// investigator's memory loop fires applyKnownFix when score >= 0.85; a
// fabricated 0.9 would falsely trigger that path on every incident.
//
// Test points:
//  1. Identical text → score very close to 1.0.
//  2. Completely unrelated text → score well below 0.85.
//  3. Scores are returned in descending order.
//  4. The actual top score reflects real similarity, not a hardcoded 0.9.
func TestVectorStore_Search_RealScores(t *testing.T) {
        vs := NewVectorStore(emb.NewTfIDF(1536))
        ctx := context.Background()

        incidentText := "KeyError: 'DATABASE_URLL' in app/main.py line 42 during startup"
        if err := vs.Add(ctx, incidentText, &VectorMetadata{
                IncidentID: "incident-1",
                Service:    "demo-app",
                RootCause:  "typo in env var name",
                FixType:    "code_fix",
                Outcome:    "success",
        }); err != nil {
                t.Fatalf("Add failed: %v", err)
        }

        if err := vs.Add(ctx, "java.lang.NullPointerException at com.example.User.fetchProfile(User.java:88)", &VectorMetadata{
                IncidentID: "incident-2",
                Service:    "user-service",
                RootCause:  "null user object",
                FixType:    "code_fix",
                Outcome:    "success",
        }); err != nil {
                t.Fatalf("Add failed: %v", err)
        }

        t.Run("identical text scores very high", func(t *testing.T) {
                metas, scores, err := vs.Search(ctx, incidentText, 2)
                if err != nil {
                        t.Fatalf("Search failed: %v", err)
                }
                if len(metas) != 2 {
                        t.Fatalf("expected 2 results, got %d", len(metas))
                }
                if scores[0] < 0.99 {
                        t.Errorf("identical text should score ~1.0, got %.4f", scores[0])
                }
                if metas[0].IncidentID != "incident-1" {
                        t.Errorf("expected top match to be incident-1, got %s", metas[0].IncidentID)
                }
        })

        t.Run("unrelated text scores below 0.85 threshold", func(t *testing.T) {
                metas, scores, err := vs.Search(ctx, "panic: runtime error: index out of range in go-http-server", 2)
                if err != nil {
                        t.Fatalf("Search failed: %v", err)
                }
                if len(metas) == 0 {
                        t.Skip("no results returned")
                }
                if scores[0] > 0.85 {
                        t.Errorf("unrelated text scored %.4f — above the 0.85 applyKnownFix threshold. "+
                                "This would falsely trigger applyKnownFix in the investigator. "+
                                "Score should reflect real cosine similarity.", scores[0])
                }
                if scores[0] == 0.9 {
                        t.Errorf("top score is exactly 0.9 — looks like the fabrication bug is back")
                }
        })

        t.Run("scores in descending order", func(t *testing.T) {
                _, scores, err := vs.Search(ctx, "KeyError DATABASE_URLL", 2)
                if err != nil {
                        t.Fatalf("Search failed: %v", err)
                }
                if len(scores) < 2 {
                        t.Skip("need 2+ results to check ordering")
                }
                for i := 1; i < len(scores); i++ {
                        if scores[i] > scores[i-1] {
                                t.Errorf("scores not in descending order: scores[%d]=%.4f > scores[%d]=%.4f",
                                        i, scores[i], i-1, scores[i-1])
                        }
                }
        })

        t.Run("no results when index empty", func(t *testing.T) {
                empty := NewVectorStore(emb.NewTfIDF(1536))
                metas, scores, err := empty.Search(ctx, "anything", 5)
                if err != nil {
                        t.Fatalf("Search on empty index failed: %v", err)
                }
                if len(metas) != 0 || len(scores) != 0 {
                        t.Errorf("expected empty results, got %d metas / %d scores", len(metas), len(scores))
                }
        })
}

// TestVectorStore_Search_ScoresAreNotFabricated is a regression guard:
// ensures the top score is never a hardcoded constant. We query with text
// that's similar (but not identical) to a stored incident — the score
// should be neither 1.0 nor a fabricated default.
func TestVectorStore_Search_ScoresAreNotFabricated(t *testing.T) {
        vs := NewVectorStore(emb.NewTfIDF(1536))
        ctx := context.Background()

        _ = vs.Add(ctx, "KeyError DATABASE_URLL typo in app main py", &VectorMetadata{
                IncidentID: "a", Outcome: "success",
        })

        _, scores, _ := vs.Search(ctx, "KeyError in main py startup", 1)
        if len(scores) == 0 {
                t.Fatal("expected non-empty scores")
        }
        if scores[0] == 0.9 {
                t.Errorf("top score is exactly 0.9 — looks like the fabrication bug is back. "+
                        "Real score for partial overlap should differ from 0.9, got %.4f", scores[0])
        }
        if scores[0] == 1.0 {
                t.Errorf("top score is 1.0 for partial overlap — unexpected. got %.4f", scores[0])
        }
}

// TestFeedback_VectorSimilarity is the v1 regression test: identical
// text scores ~1.0, semantically-overlapping text scores in the middle,
// completely disjoint text scores near 0. Verifies the TF-IDF backend
// produces a monotonic relationship between lexical overlap and cosine
// similarity.
func TestFeedback_VectorSimilarity(t *testing.T) {
        vs := NewVectorStore(emb.NewTfIDF(64))
        ctx := context.Background()

        _ = vs.Add(ctx, "KeyError DATABASE_URLL app main py", &VectorMetadata{IncidentID: "a"})
        _ = vs.Add(ctx, "TypeError missing property web server js", &VectorMetadata{IncidentID: "b"})

        // Query 1: identical to stored incident a.
        _, identical, _ := vs.Search(ctx, "KeyError DATABASE_URLL app main py", 1)
        if len(identical) == 0 || identical[0] < 0.99 {
                t.Errorf("identical text similarity = %v, want >= 0.99", identical)
        }

        // Query 2: partial overlap with incident a (3 shared tokens).
        _, partial, _ := vs.Search(ctx, "KeyError DATABASE_URLL different file", 1)
        if len(partial) == 0 {
                t.Fatal("partial overlap returned no results")
        }
        if partial[0] >= 0.99 {
                t.Errorf("partial overlap similarity = %.4f, want < 0.99 (not identical)", partial[0])
        }

        // Query 3: completely disjoint tokens.
        _, disjoint, _ := vs.Search(ctx, "completely unrelated words zzz qqq", 1)
        if len(disjoint) > 0 && disjoint[0] > 0.5 {
                t.Errorf("disjoint text similarity = %.4f, want <= 0.5", disjoint[0])
        }
}

// TestVectorStore_FIFOEviction verifies the in-memory cap evicts the
// oldest entries when exceeded. The SQLite table is the source of truth;
// only the in-memory index is bounded.
func TestVectorStore_FIFOEviction(t *testing.T) {
        vs := NewVectorStore(emb.NewTfIDF(64))
        vs.SetMaxEntries(3)
        ctx := context.Background()

        for i := 0; i < 5; i++ {
                // Use distinct text so each Add inserts a unique vector.
                text := string(rune('a'+i)) + " " + string(rune('a'+i+1)) + " " + string(rune('a'+i+2))
                if err := vs.Add(ctx, text, &VectorMetadata{IncidentID: text}); err != nil {
                        t.Fatalf("Add %d: %v", i, err)
                }
        }

        if got := vs.Len(); got != 3 {
                t.Errorf("Len = %d, want 3 (capped)", got)
        }

        // Search should not return evicted entries. The first two Adds ("a b c"
        // and "b c d") should be gone — their metadata was evicted.
        metas, _, _ := vs.Search(ctx, "a b c", 5)
        for _, m := range metas {
                if m.IncidentID == "a b c" {
                        t.Errorf("evicted entry 'a b c' was returned by Search; metadata should be missing")
                }
        }
}

// TestVectorStore_UsingHashFallback verifies the embedder-type probe.
func TestVectorStore_UsingHashFallback(t *testing.T) {
        vs := NewVectorStore(emb.NewTfIDF(64))
        if !vs.UsingHashFallback() {
                t.Error("tfidf embedder should report UsingHashFallback=true")
        }
}
