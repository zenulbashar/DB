package postgres

// Reconciler-facing store methods. These are PRIVILEGED (platform actor, not
// org-scoped): the reconciler operates across all tenants by design, and its
// binary never serves user requests (SYSTEM_ARCHITECTURE §2.1).

import (
	"context"
	"time"

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
// deleting, suspending, resuming) with the project context needed to build
// resources. The reconciler only acts on transitional states; suspending and
// resuming are the scale-to-zero convergence work (ADR-014).
func (s *Store) ListReconcileWork(ctx context.Context) ([]BranchWork, error) {
	var out []BranchWork
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT b.id, b.project_id, b.org_id, b.parent_id, b.name, b.role, b.state,
			       b.compute_min_cu, b.compute_max_cu, b.suspend_timeout_s, b.retention_days, b.created_at,
			       p.region, p.pg_version
			  FROM branches b JOIN projects p ON p.id = b.project_id
			 WHERE b.state IN ('provisioning','deleting','suspending','resuming')
			    OR (b.state = 'ready'
			        AND EXISTS (SELECT 1 FROM endpoints e
			                     WHERE e.branch_id = b.id AND e.state = 'provisioning'))
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

// MarkBranchReady flips a provisioning branch (and its endpoints) to ready. It
// seeds last_active_at so a freshly-ready branch gets a full suspend_timeout
// grace period before the idle sweep can suspend it (ADR-015).
func (s *Store) MarkBranchReady(ctx context.Context, branchID string) error {
	return s.withPrivTx(ctx, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE branches SET state = 'ready', last_active_at = now() WHERE id = $1 AND state = 'provisioning'`, branchID)
		if err != nil || ct.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE endpoints SET state = 'ready' WHERE branch_id = $1 AND state = 'provisioning'`, branchID)
		return err
	})
}

// MarkBranchSuspended flips a suspending branch (and its endpoints) to
// suspended once the reconciler has hibernated the compute (ADR-014). The
// guard on state='suspending' keeps it idempotent and safe against a stale
// re-run.
func (s *Store) MarkBranchSuspended(ctx context.Context, branchID string) error {
	return s.markBranchComputeState(ctx, branchID, "suspending", "suspended")
}

// MarkBranchResumed flips a resuming branch (and its endpoints) back to ready
// once the compute is un-hibernated and healthy.
func (s *Store) MarkBranchResumed(ctx context.Context, branchID string) error {
	return s.markBranchComputeState(ctx, branchID, "resuming", "ready")
}

// MarkEndpointsReady flips a branch's newly-provisioned endpoints (an added
// read endpoint) to ready once the reconciler has built their backing pooler +
// replica. The branch itself is untouched (it stays ready).
func (s *Store) MarkEndpointsReady(ctx context.Context, branchID string) error {
	return s.withPrivTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE endpoints SET state = 'ready' WHERE branch_id = $1 AND state = 'provisioning'`, branchID)
		return err
	})
}

func (s *Store) markBranchComputeState(ctx context.Context, branchID, from, to string) error {
	return s.withPrivTx(ctx, func(tx pgx.Tx) error {
		// Reset the idle clock when a branch returns to ready (resume), so it is
		// not immediately re-suspended by a stale last_active_at (ADR-015).
		set := "state = $2"
		if to == "ready" {
			set = "state = $2, last_active_at = now()"
		}
		ct, err := tx.Exec(ctx,
			`UPDATE branches SET `+set+` WHERE id = $1 AND state = $3`, branchID, to, from)
		if err != nil || ct.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE endpoints SET state = $2 WHERE branch_id = $1 AND state = $3`, branchID, to, from)
		return err
	})
}

// ReportGatewayActivity records one gateway replica's per-branch active
// connection counts (ADR-015). It upserts the (branch, gateway) telemetry row
// and bumps last_active_at for any branch this report shows as active, so the
// idle clock reflects the whole fleet's traffic. Privileged: called by the
// gateway via the internal API, not the tenant path.
func (s *Store) ReportGatewayActivity(ctx context.Context, gatewayID string, counts map[string]int) error {
	if gatewayID == "" || len(counts) == 0 {
		return nil
	}
	return s.withPrivTx(ctx, func(tx pgx.Tx) error {
		var active []string
		for branchID, n := range counts {
			if _, err := tx.Exec(ctx, `
				INSERT INTO branch_activity (branch_id, gateway_id, active_conns, reported_at)
				VALUES ($1, $2, $3, now())
				ON CONFLICT (branch_id, gateway_id)
				DO UPDATE SET active_conns = EXCLUDED.active_conns, reported_at = now()`,
				branchID, gatewayID, n); err != nil {
				return err
			}
			if n > 0 {
				active = append(active, branchID)
			}
		}
		if len(active) > 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE branches SET last_active_at = now()
				  WHERE id = ANY($1) AND state IN ('ready', 'resuming')`, active); err != nil {
				return err
			}
		}
		return nil
	})
}

// SweepIdleBranches flips ready branches that have been globally idle past their
// suspend_timeout to suspending (the reconciler then hibernates them; ADR-015).
// "Globally idle" = the active-connection count summed across all gateways that
// reported within staleWindow is zero. It is FAIL-SAFE: if no gateway has
// reported within staleWindow it does nothing, so activity-reporting downtime
// can never mass-suspend the fleet. Returns the branch ids it flipped.
func (s *Store) SweepIdleBranches(ctx context.Context, staleWindow time.Duration) ([]string, error) {
	stale := staleWindow.Seconds()
	var flipped []string
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		var reportingLive bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM branch_activity WHERE reported_at > now() - make_interval(secs => $1))`,
			stale).Scan(&reportingLive); err != nil {
			return err
		}
		if !reportingLive {
			return nil // blind — never suspend when no gateway is reporting
		}
		rows, err := tx.Query(ctx, `
			SELECT b.id FROM branches b
			 WHERE b.state = 'ready' AND b.suspend_timeout_s > 0
			   AND (b.last_active_at IS NULL
			        OR b.last_active_at < now() - make_interval(secs => b.suspend_timeout_s))
			   AND COALESCE((
			        SELECT sum(a.active_conns) FROM branch_activity a
			         WHERE a.branch_id = b.id AND a.reported_at > now() - make_interval(secs => $1)
			   ), 0) = 0
			 ORDER BY b.id
			 FOR UPDATE OF b SKIP LOCKED`, stale)
		if err != nil {
			return err
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE branches SET state = 'suspending' WHERE id = ANY($1) AND state = 'ready'`, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE endpoints SET state = 'suspending' WHERE branch_id = ANY($1) AND state = 'ready'`, ids); err != nil {
			return err
		}
		flipped = ids
		return nil
	})
	return flipped, err
}

// FinishBranchTeardown removes a deleting branch's rows after its Kubernetes
// resources are gone. Rows are hard-deleted (audit_log keeps history; backup
// retention is object-storage lifecycle, not row retention).
//
// db_roles/databases/endpoints cascade on the branch delete, but the secrets
// those roles reference do NOT (the FK points db_roles -> secrets), so they
// would orphan. Delete them explicitly first (audit finding). The parent_id
// and imports.target_branch_id references are handled by 0005's ON DELETE SET
// NULL, so the branch delete no longer needs those cleared by hand.
func (s *Store) FinishBranchTeardown(ctx context.Context, branchID string) error {
	return s.withPrivTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE projects SET default_branch_id = NULL WHERE default_branch_id = $1`, branchID); err != nil {
			return err
		}
		// Capture the roles' secret ids BEFORE deleting the branch — db_roles
		// cascade-delete with the branch (db_roles.secret_id -> secrets, so we
		// cannot delete the secrets while the roles still reference them, and
		// once the roles are gone the ids are lost).
		var secretIDs []string
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(array_agg(secret_id), '{}') FROM db_roles WHERE branch_id = $1`,
			branchID).Scan(&secretIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM branches WHERE id = $1 AND state = 'deleting'`, branchID); err != nil {
			return err
		}
		if len(secretIDs) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM secrets WHERE id = ANY($1)`, secretIDs); err != nil {
				return err
			}
		}
		return nil
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
	MaxConns   int                  // per-endpoint connection cap (0 = unlimited)
}

// ListRoutableEndpoints returns endpoints whose branches are live enough to
// route. It admits ready, suspended, AND the transitional suspending/resuming
// states — a resuming branch MUST stay in the route table so the gateway can
// hold and wake a connecting client; dropping it would surface as "endpoint not
// found" instead of a clean suspended answer (ADR-014). The transitional states
// are mapped down to "suspended" for the gateway in BuildRoutesJSON. MaxConns
// is derived from the branch's compute ceiling (audit finding: it was always
// 0/unlimited).
func (s *Store) ListRoutableEndpoints(ctx context.Context) ([]RoutableEndpoint, error) {
	var out []RoutableEndpoint
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT e.id, e.branch_id, b.project_id, e.kind, e.state, b.compute_max_cu
			  FROM endpoints e JOIN branches b ON b.id = e.branch_id
			 WHERE e.state IN ('ready','suspending','suspended','resuming')
			   AND b.state IN ('ready','suspending','suspended','resuming')
			 ORDER BY e.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r RoutableEndpoint
			var maxCU float64
			if err := rows.Scan(&r.EndpointID, &r.BranchID, &r.ProjectID, &r.Kind, &r.State, &maxCU); err != nil {
				return err
			}
			r.MaxConns = MaxConnsForEndpoint(r.Kind, maxCU)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// MaxConnsForEndpoint sizes the gateway connection cap from the branch compute
// ceiling. Postgres max_connections scales roughly with CUs; the pooled
// endpoint multiplexes so it gets the larger cap. These bound the shared L4
// tier (MULTI_TENANCY §3) without starving a legitimately busy tenant.
func MaxConnsForEndpoint(kind domain.EndpointKind, maxCU float64) int {
	perCU := 50
	base := int(maxCU * float64(perCU))
	if base < 25 {
		base = 25
	}
	if kind == domain.EndpointRWPooled || kind == domain.EndpointROPooled {
		return base * 4 // pooled clients are many-but-short
	}
	return base
}
