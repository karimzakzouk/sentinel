package feedback

import (
        "context"
        "testing"

        "github.com/karimzakzouk/sentinel/internal/emb"
)

// TestVectorStore_Search_RealScores verifies that v3.3.1 fix: VectorStore.Search
// returns REAL cosine similarity scores (not fabricated ones).
// Before v3.3.1, the adapter in main.go fabricated scores as
//   scores[0] = 0.9, scores[i] = 0.5 - 0.1*i
// which made the investigator's memory loop fire applyKnownFix on every incident
// after the first success, because the 0.85 threshold was always exceeded.
// This test verifies:
//  1. Identical text → score very close to 1.0
//  2. Completely unrelated text → score well below 0.85
//  3. Scores are returned in descending order (HNSW sorts by ascending distance)
//  4. The actual top score reflects real similarity, not a hardcoded 0.9
func TestVectorStore_Search_RealScores(t *testing.T) {
        // No API key → falls back to hashEmbedding (deterministic, no network).
        vs := NewVectorStore(emb.NewTfIDF(1536))
        ctx := context.Background()

        // Insert a known incident.
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

        // Insert an unrelated incident.
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
                // Identical text should produce a cosine similarity of ~1.0.
                // hashEmbedding is deterministic, so this is exact.
                if scores[0] < 0.99 {
                        t.Errorf("identical text should score ~1.0, got %.4f", scores[0])
                }
                if metas[0].IncidentID != "incident-1" {
                        t.Errorf("expected top match to be incident-1, got %s", metas[0].IncidentID)
                }
        })

        t.Run("unrelated text scores below 0.85 threshold", func(t *testing.T) {
                // Search with completely different content — should NOT trigger
                // the applyKnownFix path (>0.85 threshold) in investigator.go.
                metas, scores, err := vs.Search(ctx, "panic: runtime error: index out of range in go-http-server", 2)
                if err != nil {
                        t.Fatalf("Search failed: %v", err)
                }
                if len(metas) == 0 {
                        t.Skip("no results returned")
                }
                // The top score must be REAL similarity, not the fabricated 0.9.
                // If the bug were still present, this would always be 0.9.
                if scores[0] > 0.85 {
                        t.Errorf("unrelated text scored %.4f — above the 0.85 applyKnownFix threshold. "+
                                "This would falsely trigger applyKnownFix in the investigator. "+
                                "Score should reflect real cosine similarity.", scores[0])
                }
                // Sanity: also assert it's NOT the old fabricated 0.9.
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

// TestVectorStore_Search_ScoresAreNotFabricated is a regression guard: ensures
// the top score is NEVER the hardcoded 0.9 from the pre-v3.3.1 bug.
// We query with text that's similar (but not identical) to a stored incident —
// the score should be neither 1.0 (exact match) nor the fabricated 0.9.
func TestVectorStore_Search_ScoresAreNotFabricated(t *testing.T) {
        vs := NewVectorStore(emb.NewTfIDF(1536))
        ctx := context.Background()

        _ = vs.Add(ctx, "KeyError DATABASE_URLL typo in app main py", &VectorMetadata{
                IncidentID: "a", Outcome: "success",
        })

        // Query with overlapping words but not identical.
        _, scores, _ := vs.Search(ctx, "KeyError in main py startup", 1)
        if len(scores) == 0 {
                t.Fatal("expected non-empty scores")
        }
        if scores[0] == 0.9 {
                t.Errorf("top score is exactly 0.9 — looks like the pre-v3.3.1 fabrication bug is back. "+
                        "Real score for partial overlap should differ from 0.9, got %.4f", scores[0])
        }
        if scores[0] == 1.0 {
                t.Errorf("top score is 1.0 for partial overlap — unexpected. got %.4f", scores[0])
        }
}
