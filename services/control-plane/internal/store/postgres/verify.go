package postgres

// Restore-verification store methods (R-2). PRIVILEGED — driven by the
// reconciler's verify loop, never by tenant requests.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// RestoreVerification is the latest verification attempt for a branch.
type RestoreVerification struct {
	BranchID   string
	OrgID      string
	ProjectID  string
	Status     string // running | pass | fail
	Message    string
	StartedAt  time.Time
	VerifiedAt *time.Time
}

// ListRestoreVerifyDue returns ready branches whose archive has not been
// verified within `every` (or ever), oldest verification first, capped at
// limit. Branches with a verification currently running are excluded — the
// verify loop settles those via ListRunningRestoreVerifications.
func (s *Store) ListRestoreVerifyDue(ctx context.Context, every time.Duration, limit int) ([]BranchWork, error) {
	if limit <= 0 || limit > 10 {
		limit = 3
	}
	var out []BranchWork
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT b.id, b.project_id, b.org_id, b.parent_id, b.name, b.role, b.state,
			       b.compute_min_cu, b.compute_max_cu, COALESCE(b.current_cu, 0), b.suspend_timeout_s, b.retention_days,
			       b.bootstrap_at, b.created_at,
			       p.region, p.pg_version
			  FROM branches b
			  JOIN projects p ON p.id = b.project_id
			  LEFT JOIN restore_verifications v ON v.branch_id = b.id
			 WHERE b.state = 'ready'
			   AND (v.branch_id IS NULL
			        OR (v.status <> 'running'
			            AND v.verified_at < now() - make_interval(secs => $1)))
			 ORDER BY v.verified_at NULLS FIRST, b.id
			 LIMIT $2`, every.Seconds(), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w BranchWork
			b := &w.Branch
			if err := rows.Scan(&b.ID, &b.ProjectID, &b.OrgID, &b.ParentID, &b.Name, &b.Role, &b.State,
				&b.Compute.MinCU, &b.Compute.MaxCU, &b.Compute.CurrentCU, &b.Compute.SuspendTimeoutS, &b.RetentionDays,
				&b.BootstrapAt, &b.CreatedAt,
				&w.Region, &w.PGVersion); err != nil {
				return err
			}
			w.ProjectID, w.OrgID = b.ProjectID, b.OrgID
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

// StartRestoreVerification marks a branch's verification as running (upsert —
// a re-verify replaces the previous outcome's row).
func (s *Store) StartRestoreVerification(ctx context.Context, branchID string) error {
	return s.withPrivTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO restore_verifications (branch_id, org_id, status, message, started_at, verified_at)
			SELECT id, org_id, 'running', '', now(), NULL FROM branches WHERE id = $1
			ON CONFLICT (branch_id) DO UPDATE
			   SET status = 'running', message = '', started_at = now(), verified_at = NULL`,
			branchID)
		return err
	})
}

// FinishRestoreVerification records the outcome of a running verification.
func (s *Store) FinishRestoreVerification(ctx context.Context, branchID string, pass bool, message string) error {
	status := "fail"
	if pass {
		status = "pass"
	}
	return s.withPrivTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE restore_verifications
			   SET status = $2, message = $3, verified_at = now()
			 WHERE branch_id = $1`, branchID, status, message)
		return err
	})
}

// ListRunningRestoreVerifications returns in-flight verifications with the
// project context needed to locate their verify clusters.
func (s *Store) ListRunningRestoreVerifications(ctx context.Context) ([]RestoreVerification, error) {
	var out []RestoreVerification
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT v.branch_id, v.org_id, b.project_id, v.status, v.message, v.started_at, v.verified_at
			  FROM restore_verifications v
			  JOIN branches b ON b.id = v.branch_id
			 WHERE v.status = 'running'
			 ORDER BY v.started_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v RestoreVerification
			if err := rows.Scan(&v.BranchID, &v.OrgID, &v.ProjectID, &v.Status, &v.Message,
				&v.StartedAt, &v.VerifiedAt); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}
