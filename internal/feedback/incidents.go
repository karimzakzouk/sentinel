// Package feedback — incident CRUD operations.
package feedback

import (
        "github.com/karimzakzouk/sentinel/internal/logging"
        "context"
        "database/sql"
        "encoding/json"
        "fmt"
        "strings"
        "time"

        "github.com/google/uuid"

        "github.com/karimzakzouk/sentinel/internal/schemas"
)

func (s *Store) UpsertIncident(ctx context.Context, inc *schemas.Incident) error {
        s.mu.Lock()
        defer s.mu.Unlock()

        clusterJSON, err := json.Marshal(inc.Cluster)
        if err != nil {
                return fmt.Errorf("marshal cluster: %w", err)
        }
        stateJSON, err := json.Marshal(inc.State)
        if err != nil {
                return fmt.Errorf("marshal state: %w", err)
        }

        now := time.Now().UTC()
        inc.UpdatedAt = now

        _, err = s.db.ExecContext(ctx, `
                INSERT INTO incidents (incident_id, created_at, updated_at, service, namespace, status, severity, cluster_json, state_json, beta_alpha, beta_beta, git_commit_at_resolution, postmortem)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(incident_id) DO UPDATE SET
                        updated_at = excluded.updated_at,
                        status = excluded.status,
                        severity = excluded.severity,
                        cluster_json = excluded.cluster_json,
                        state_json = excluded.state_json,
                        beta_alpha = excluded.beta_alpha,
                        beta_beta = excluded.beta_beta,
                        git_commit_at_resolution = excluded.git_commit_at_resolution,
                        postmortem = excluded.postmortem
        `,
                inc.IncidentID.String(), inc.CreatedAt, inc.UpdatedAt,
                inc.Cluster.Service, inc.Cluster.Namespace, string(inc.Status),
                string(inc.Cluster.Severity), string(clusterJSON), string(stateJSON),
                inc.BetaAlpha, inc.BetaBeta,
                nullableString(inc.GitCommitAtResolution),
                nullableString(inc.Postmortem),
        )
        return err
}

// GetIncident retrieves a single incident by ID.

func (s *Store) GetIncident(ctx context.Context, id uuid.UUID) (*schemas.Incident, error) {
        s.mu.RLock()
        defer s.mu.RUnlock()

        row := s.db.QueryRowContext(ctx, `
                SELECT incident_id, created_at, updated_at, service, namespace, status, severity, cluster_json, state_json, beta_alpha, beta_beta, git_commit_at_resolution, postmortem
                FROM incidents WHERE incident_id = ?
        `, id.String())

        var inc schemas.Incident
        var incidentIDStr, statusStr, severityStr, clusterJSON, stateJSON string
        var betaAlpha, betaBeta float64
        var createdAt, updatedAt time.Time
        var gitCommit, postmortem sql.NullString

        if err := row.Scan(
                &incidentIDStr, &createdAt, &updatedAt,
                &inc.Cluster.Service, &inc.Cluster.Namespace, &statusStr, &severityStr,
                &clusterJSON, &stateJSON, &betaAlpha, &betaBeta,
                &gitCommit, &postmortem,
        ); err != nil {
                if err == sql.ErrNoRows {
                        return nil, nil
                }
                return nil, err
        }

        parsedID, err := uuid.Parse(incidentIDStr)
        if err != nil {
                return nil, fmt.Errorf("parse incident ID: %w", err)
        }
        inc.IncidentID = parsedID
        inc.CreatedAt = createdAt
        inc.UpdatedAt = updatedAt
        inc.Status = schemas.IncidentStatus(statusStr)
        inc.Cluster.Severity = schemas.Severity(severityStr)
        inc.BetaAlpha = betaAlpha
        inc.BetaBeta = betaBeta
        if gitCommit.Valid {
                s := gitCommit.String
                inc.GitCommitAtResolution = &s
        }
        if postmortem.Valid {
                s := postmortem.String
                inc.Postmortem = &s
        }

        if err := json.Unmarshal([]byte(clusterJSON), &inc.Cluster); err != nil {
                return nil, fmt.Errorf("unmarshal cluster: %w", err)
        }
        if err := json.Unmarshal([]byte(stateJSON), &inc.State); err != nil {
                return nil, fmt.Errorf("unmarshal state: %w", err)
        }

        return &inc, nil
}

// FindIncidentByPrefix finds an incident whose UUID starts with the given
// prefix. If multiple incidents match, returns the most recently updated one.
// Returns nil (no error) if no match.
// in `incidents list` output. Without this, `sentinelctl incidents get
// 91cce5a5` would fail with "invalid incident ID" because the server did
// uuid.Parse("91cce5a5") which requires a full 36-char UUID.
// The prefix must be at least 4 chars to prevent accidental collisions on
// short prefixes (a 1-char prefix would match ~1/16 of all UUIDs).

func (s *Store) FindIncidentByPrefix(ctx context.Context, prefix string) (*schemas.Incident, error) {
        if len(prefix) < 4 {
                return nil, nil
        }
        // Normalize: lowercase, strip hyphens so "91cce5a5" and "91cce5a5-1234"
        // both work against stored UUIDs (which are stored as lowercase strings
        // with hyphens).

        cleaned := strings.ToLower(strings.ReplaceAll(prefix, "-", ""))
        if len(cleaned) > 36 {
                return nil, nil
        }

        s.mu.RLock()
        defer s.mu.RUnlock()

        // Pull the most recent 500 incidents and scan in Go. SQLite's LIKE on
        // a hyphenated UUID is awkward (the stored form has hyphens, the prefix
        // doesn't), and an index on incident_id won't help with prefix matches
        // anyway. 500 is plenty — if you have >500 recent incidents, use the
        // full UUID.

        rows, err := s.db.QueryContext(ctx, `
                SELECT incident_id, created_at, updated_at, service, namespace, status, severity, cluster_json, state_json, beta_alpha, beta_beta, git_commit_at_resolution, postmortem
                FROM incidents ORDER BY updated_at DESC LIMIT 500
        `)
        if err != nil {
                return nil, err
        }
        defer rows.Close()

        var best *schemas.Incident
        var bestUpdatedAt time.Time
        for rows.Next() {
                inc, updatedAt, err := scanIncidentRow(rows)
                if err != nil {
                        return nil, err
                }
                // Compare against the prefix. The stored incident_id is a full
                // UUID string like "91cce5a5-1234-5678-9abc-def012345678".
                // Strip its hyphens too for the comparison.

                stored := strings.ReplaceAll(strings.ToLower(inc.IncidentID.String()), "-", "")
                if strings.HasPrefix(stored, cleaned) {
                        if best == nil || updatedAt.After(bestUpdatedAt) {
                                best = inc
                                bestUpdatedAt = updatedAt
                        }
                }
        }
        return best, nil
}

// scanIncidentRow is the shared row-scan logic for GetIncident,
// FindIncidentByPrefix, and ListRecentIncidents. Takes a sql.Rows (so it
// works for both single-row QueryRow and multi-row Query).

type rowScanner interface {
        Scan(dest ...any) error
}

func scanIncidentRow(row rowScanner) (*schemas.Incident, time.Time, error) {
        var inc schemas.Incident
        var incidentIDStr, statusStr, severityStr, clusterJSON, stateJSON string
        var betaAlpha, betaBeta float64
        var createdAt, updatedAt time.Time
        var gitCommit, postmortem sql.NullString

        if err := row.Scan(
                &incidentIDStr, &createdAt, &updatedAt,
                &inc.Cluster.Service, &inc.Cluster.Namespace, &statusStr, &severityStr,
                &clusterJSON, &stateJSON, &betaAlpha, &betaBeta,
                &gitCommit, &postmortem,
        ); err != nil {
                return nil, time.Time{}, err
        }

        parsedID, err := uuid.Parse(incidentIDStr)
        if err != nil {
                return nil, time.Time{}, fmt.Errorf("parse incident ID: %w", err)
        }
        inc.IncidentID = parsedID
        inc.CreatedAt = createdAt
        inc.UpdatedAt = updatedAt
        inc.Status = schemas.IncidentStatus(statusStr)
        inc.Cluster.Severity = schemas.Severity(severityStr)
        inc.BetaAlpha = betaAlpha
        inc.BetaBeta = betaBeta
        if gitCommit.Valid {
                s := gitCommit.String
                inc.GitCommitAtResolution = &s
        }
        if postmortem.Valid {
                s := postmortem.String
                inc.Postmortem = &s
        }

        if err := json.Unmarshal([]byte(clusterJSON), &inc.Cluster); err != nil {
                return nil, time.Time{}, fmt.Errorf("unmarshal cluster: %w", err)
        }
        if err := json.Unmarshal([]byte(stateJSON), &inc.State); err != nil {
                return nil, time.Time{}, fmt.Errorf("unmarshal state: %w", err)
        }
        return &inc, updatedAt, nil
}
func (s *Store) ListRecentIncidents(ctx context.Context, limit int) ([]*schemas.Incident, error) {
        s.mu.RLock()
        defer s.mu.RUnlock()

        if limit <= 0 || limit > 500 {
                limit = 50
        }

        rows, err := s.db.QueryContext(ctx, `
                SELECT incident_id, created_at, updated_at, service, namespace, status, severity, cluster_json, state_json, beta_alpha, beta_beta, git_commit_at_resolution, postmortem
                FROM incidents ORDER BY updated_at DESC LIMIT ?
        `, limit)
        if err != nil {
                return nil, err
        }
        defer rows.Close()

        var out []*schemas.Incident
        for rows.Next() {
                var inc schemas.Incident
                var incidentIDStr, statusStr, severityStr, clusterJSON, stateJSON string
                var betaAlpha, betaBeta float64
                var createdAt, updatedAt time.Time
                var gitCommit, postmortem sql.NullString

                if err := rows.Scan(
                        &incidentIDStr, &createdAt, &updatedAt,
                        &inc.Cluster.Service, &inc.Cluster.Namespace, &statusStr, &severityStr,
                        &clusterJSON, &stateJSON, &betaAlpha, &betaBeta,
                        &gitCommit, &postmortem,
                ); err != nil {
                        return nil, err
                }

                parsedID, err := uuid.Parse(incidentIDStr)
                if err != nil {
                        logging.Info("skipping row with invalid UUID", "value", incidentIDStr, err)
                        continue
                }
                inc.IncidentID = parsedID
                inc.CreatedAt = createdAt
                inc.UpdatedAt = updatedAt
                inc.Status = schemas.IncidentStatus(statusStr)
                inc.Cluster.Severity = schemas.Severity(severityStr)
                inc.BetaAlpha = betaAlpha
                inc.BetaBeta = betaBeta
                if gitCommit.Valid {
                        s := gitCommit.String
                        inc.GitCommitAtResolution = &s
                }
                if postmortem.Valid {
                        s := postmortem.String
                        inc.Postmortem = &s
                }

                if err := json.Unmarshal([]byte(clusterJSON), &inc.Cluster); err != nil {
                        logging.Info("skipping incident with corrupt cluster JSON", "value", parsedID, err)
                        continue
                }
                if err := json.Unmarshal([]byte(stateJSON), &inc.State); err != nil {
                        continue
                }
                out = append(out, &inc)
        }
        return out, rows.Err()
}

// AppendEvent appends an audit event to the event log.


func (s *Store) FindIncidentByPR(ctx context.Context, repo string, prNumber int) (*schemas.Incident, error) {
        s.mu.RLock()
        defer s.mu.RUnlock()

        // Try the dedicated pr_repo/pr_number columns first (v4.0.0 migration).

        row := s.db.QueryRowContext(ctx, `
                SELECT incident_id, created_at, updated_at, service, namespace, status, severity, cluster_json, state_json, beta_alpha, beta_beta, git_commit_at_resolution, postmortem
                FROM incidents
                WHERE pr_repo = ? AND pr_number = ?
                ORDER BY updated_at DESC
                LIMIT 1
        `, repo, prNumber)

        inc, _, err := scanIncidentRow(row)
        if err == nil && inc != nil {
                return inc, nil
        }
        if err != nil && err != sql.ErrNoRows {
                return nil, err
        }

        // Fallback: scan state_json for matching PR info (older incidents
        // from before the v4.0.0 migration that haven't been updated).

        rows, err := s.db.QueryContext(ctx, `
                SELECT incident_id, created_at, updated_at, service, namespace, status, severity, cluster_json, state_json, beta_alpha, beta_beta, git_commit_at_resolution, postmortem
                FROM incidents
                WHERE status IN ('verified', 'resolved', 'executing', 'fix_proposed', 'approved')
                ORDER BY updated_at DESC
                LIMIT 200
        `)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        for rows.Next() {
                inc, _, err := scanIncidentRow(rows)
                if err != nil {
                        logging.Error("skipping incident row with scan error", "err", err)
                        continue
                }
                // Check if this incident's PR matches

                if inc.State.ExecutionResult != nil && inc.State.ExecutionResult.PRNumber != nil &&
                        *inc.State.ExecutionResult.PRNumber == prNumber {
                        if fp := inc.State.FixProposal; fp != nil && fp.Repo != nil {
                                if normalizeRepo(*fp.Repo) == normalizeRepo(repo) {
                                        return inc, nil
                                }
                        }
                        return inc, nil
                }
        }
        return nil, nil
}

// normalizeRepo lowercases + strips trailing .git for repo comparison.

func normalizeRepo(r string) string {
        r = strings.ToLower(strings.TrimSpace(r))
        r = strings.TrimSuffix(r, ".git")
        return r
}

// UpdateIncidentPR associates an incident with a PR number + repo.
// Called after the GitHub executor opens a PR, so chatops commands on that
// PR can find the incident.

func (s *Store) UpdateIncidentPR(ctx context.Context, incidentID uuid.UUID, repo string, prNumber int, prBranch, prURL string) error {
        s.mu.Lock()
        defer s.mu.Unlock()
        _, err := s.db.ExecContext(ctx, `
                UPDATE incidents SET pr_repo = ?, pr_number = ?, pr_branch = ?, pr_url = ?, updated_at = ?
                WHERE incident_id = ?
        `, repo, prNumber, nullableString(&prBranch), nullableString(&prURL), time.Now().UTC(), incidentID.String())
        return err
}

// UpdateAutoMergeStatus sets the auto_merge_status column on an incident.
// Values: "not_eligible", "pending_ci", "merged", "skipped:<reason>", "regressed".

func (s *Store) UpdateAutoMergeStatus(ctx context.Context, incidentID uuid.UUID, status string) error {
        s.mu.Lock()
        defer s.mu.Unlock()
        _, err := s.db.ExecContext(ctx, `
                UPDATE incidents SET auto_merge_status = ?, updated_at = ?
                WHERE incident_id = ?
        `, status, time.Now().UTC(), incidentID.String())
        return err
}

// UpdateOverrides sets admin override flags on an incident's ExecutionResult.
// Called by /sentinel override sandbox|ci. When true, the auto-merge policy
// treats the corresponding gate as passed without actually running the check.
// The overrides are stored inside state_json (ExecutionResult.SandboxOverride
// / CIOverride), so we read-modify-write the state.
func (s *Store) UpdateOverrides(ctx context.Context, incidentID uuid.UUID, sandboxOverride, ciOverride bool) error {
        s.mu.Lock()
        defer s.mu.Unlock()

        // Read current state_json
        var stateJSON string
        err := s.db.QueryRowContext(ctx,
                `SELECT state_json FROM incidents WHERE incident_id = ?`,
                incidentID.String(),
        ).Scan(&stateJSON)
        if err != nil {
                return fmt.Errorf("read state for override: %w", err)
        }

        // Unmarshal, set overrides, re-marshal
        var state schemas.AgentState
        if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
                return fmt.Errorf("unmarshal state for override: %w", err)
        }
        if state.ExecutionResult == nil {
                state.ExecutionResult = &schemas.ExecutionResult{}
        }
        state.ExecutionResult.SandboxOverride = sandboxOverride
        state.ExecutionResult.CIOverride = ciOverride

        newStateJSON, err := json.Marshal(state)
        if err != nil {
                return fmt.Errorf("marshal state for override: %w", err)
        }

        _, err = s.db.ExecContext(ctx,
                `UPDATE incidents SET state_json = ?, updated_at = ? WHERE incident_id = ?`,
                string(newStateJSON), time.Now().UTC(), incidentID.String())
        return err
}

// LogCommandEvent writes a chatops command invocation to the audit log.

