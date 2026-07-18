package postgres

// Reconciler-facing store methods. These are PRIVILEGED (platform actor, not
// org-scoped): the reconciler operates across all tenants by design, and its
// binary never serves user requests (SYSTEM_ARCHITECTURE §2.1).

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
)

// BranchWork is one branch the reconciler must converge.
type BranchWork struct {
	Branch    domain.Branch
	ProjectID string
	OrgID     string
	Region    string
	PGVersion int
	Endpoints []domain.Endpoint
}

// ListReconcileWork returns branches in transitional states (provisioning,
// deleting) with the project context needed to build resources.
func (s *Store) ListReconcileWork(ctx context.Context) ([]BranchWork, error) {
	var out []BranchWork
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT b.id, b.project_id, b.org_id, b.parent_id, b.name, b.role, b.state,
			       b.compute_min_cu, b.compute_max_cu, b.suspend_timeout_s, b.retention_days, b.created_at,
			       p.region, p.pg_version
			  FROM branches b JOIN projects p ON p.id = b.project_id
			 WHERE b.state IN ('provisioning','deleting')
			 ORDER BY b.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w BranchWork
			b := &w.Branch
			if err := rows.Scan(&b.ID, &b.ProjectID, &b.OrgID, &b.ParentID, &b.Name, &b.Role, &b.State,
				&b.Compute.MinCU, &b.Compute.MaxCU, &b.Compute.SuspendTimeoutS, &b.RetentionDays, &b.CreatedAt,
				&w.Region, &w.PGVersion); err != nil {
				return err
			}
			w.ProjectID, w.OrgID = b.ProjectID, b.OrgID
			out = append(out, w)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			eps, err := listEndpointsTx(ctx, tx, out[i].Branch.ID)
			if err != nil {
				return err
			}
			out[i].Endpoints = eps
		}
		return nil
	})
	return out, err
}

// MarkBranchReady flips a provisioning branch (and its endpoints) to ready.
func (s *Store) MarkBranchReady(ctx context.Context, branchID string) error {
	return s.withPrivTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE branches SET state = 'ready' WHERE id = $1 AND state = 'provisioning'`, branchID)
		if err != nil || ct.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE endpoints SET state = 'ready' WHERE branch_id = $1 AND state = 'provisioning'`, branchID)
		return err
	})
}

// FinishBranchTeardown removes a deleting branch's rows after its Kubernetes
// resources are gone. Rows are hard-deleted (audit_log keeps history; backup
// retention is object-storage lifecycle, not row retention).
func (s *Store) FinishBranchTeardown(ctx context.Context, branchID string) error {
	return s.withPrivTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE projects SET default_branch_id = NULL WHERE default_branch_id = $1`, branchID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM branches WHERE id = $1 AND state = 'deleting'`, branchID)
		return err
	})
}

// CountLiveBranches reports non-deleting branches remaining in a project —
// zero means the project namespace can go.
func (s *Store) CountLiveBranches(ctx context.Context, projectID string) (int, error) {
	var n int
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM branches WHERE project_id = $1 AND state <> 'deleting'`, projectID).Scan(&n)
	})
	return n, err
}

// RoutableEndpoint feeds the gateway route table.
type RoutableEndpoint struct {
	EndpointID string
	BranchID   string
	ProjectID  string
	Kind       domain.EndpointKind
	State      domain.ResourceState // ready or suspended reach the table
}

// ListRoutableEndpoints returns endpoints whose branches are live enough to
// route (ready or suspended — suspended entries let the gateway answer with
// a clear error today and wake-on-connect in Phase 4).
func (s *Store) ListRoutableEndpoints(ctx context.Context) ([]RoutableEndpoint, error) {
	var out []RoutableEndpoint
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT e.id, e.branch_id, b.project_id, e.kind, e.state
			  FROM endpoints e JOIN branches b ON b.id = e.branch_id
			 WHERE e.state IN ('ready','suspended') AND b.state IN ('ready','suspended')
			 ORDER BY e.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RoutableEndpoint
			if err := rows.Scan(&r.EndpointID, &r.BranchID, &r.ProjectID, &r.Kind, &r.State); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
