package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

// Platform-operator reads (ADR-018). Every query runs privileged
// (app.privileged) — these back the NDB_ADMIN_TOKEN surface only.

// effectiveCU mirrors domain.Compute.EffectiveCU in SQL: the running size if a
// resize has happened, else the floor the branch wakes at.
const effectiveCU = `CASE WHEN COALESCE(b.current_cu, 0) > 0 THEN b.current_cu ELSE b.compute_min_cu END`

// runningStates are the branch states that hold compute (suspended/deleting/
// error hold none; storage-only footprint is not counted as CU).
const runningStates = `('provisioning','ready','suspending','resuming','resizing')`

// importRunningStates are the non-terminal import states.
const importRunningStates = `('pending','preflight','schema_copy','initial_copy','live_sync','cutover_ready')`

func (s *Store) AdminOverview(ctx context.Context) (*domain.AdminOverview, error) {
	o := &domain.AdminOverview{
		BranchesByState: map[string]int{},
		ImportsByState:  map[string]int{},
	}
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM orgs),
			       (SELECT count(*) FROM users),
			       (SELECT count(*) FROM projects WHERE state <> 'deleting'),
			       (SELECT count(*) FROM branches WHERE state <> 'deleting'),
			       (SELECT count(*) FROM endpoints WHERE state <> 'deleting'),
			       (SELECT count(*) FROM api_keys
			         WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())),
			       (SELECT COALESCE(sum(`+effectiveCU+`), 0) FROM branches b
			         WHERE b.state IN `+runningStates+`)`).
			Scan(&o.Orgs, &o.Users, &o.Projects, &o.Branches, &o.Endpoints,
				&o.ActiveAPIKeys, &o.AllocatedCU); err != nil {
			return err
		}
		if err := countByState(ctx, tx,
			`SELECT state, count(*) FROM branches WHERE state <> 'deleting' GROUP BY state`,
			o.BranchesByState); err != nil {
			return err
		}
		return countByState(ctx, tx,
			`SELECT state, count(*) FROM imports GROUP BY state`, o.ImportsByState)
	})
	if err != nil {
		return nil, err
	}
	return o, nil
}

func countByState(ctx context.Context, tx pgx.Tx, q string, into map[string]int) error {
	rows, err := tx.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return err
		}
		into[state] = n
	}
	return rows.Err()
}

func (s *Store) AdminListOrgUsage(ctx context.Context) ([]domain.OrgUsage, error) {
	var out []domain.OrgUsage
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT o.id, o.name, o.slug, o.plan, o.created_at,
			       (SELECT count(*) FROM org_members m WHERE m.org_id = o.id),
			       (SELECT count(*) FROM projects p WHERE p.org_id = o.id AND p.state <> 'deleting'),
			       (SELECT count(*) FROM branches b WHERE b.org_id = o.id AND b.state <> 'deleting'),
			       (SELECT count(*) FROM endpoints e WHERE e.org_id = o.id AND e.state <> 'deleting'),
			       (SELECT count(*) FROM api_keys k WHERE k.org_id = o.id
			         AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > now())),
			       (SELECT count(*) FROM imports i WHERE i.org_id = o.id
			         AND i.state IN `+importRunningStates+`),
			       (SELECT COALESCE(sum(`+effectiveCU+`), 0) FROM branches b
			         WHERE b.org_id = o.id AND b.state IN `+runningStates+`),
			       (SELECT max(b.last_active_at) FROM branches b
			         WHERE b.org_id = o.id AND b.state <> 'deleting')
			  FROM orgs o ORDER BY o.created_at, o.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u domain.OrgUsage
			u.BranchesByState = map[string]int{}
			if err := rows.Scan(&u.Org.ID, &u.Org.Name, &u.Org.Slug, &u.Org.Plan, &u.Org.CreatedAt,
				&u.Members, &u.Projects, &u.Branches, &u.Endpoints,
				&u.ActiveAPIKeys, &u.ImportsRunning, &u.AllocatedCU, &u.LastActiveAt); err != nil {
				return err
			}
			out = append(out, u)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// Per-org branch-state histograms in one pass.
		byOrg := map[string]map[string]int{}
		brows, err := tx.Query(ctx,
			`SELECT org_id, state, count(*) FROM branches WHERE state <> 'deleting' GROUP BY org_id, state`)
		if err != nil {
			return err
		}
		defer brows.Close()
		for brows.Next() {
			var org, state string
			var n int
			if err := brows.Scan(&org, &state, &n); err != nil {
				return err
			}
			if byOrg[org] == nil {
				byOrg[org] = map[string]int{}
			}
			byOrg[org][state] = n
		}
		if err := brows.Err(); err != nil {
			return err
		}
		for i := range out {
			if m := byOrg[out[i].Org.ID]; m != nil {
				out[i].BranchesByState = m
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) AdminListBranches(ctx context.Context, state string, limit int) ([]domain.AdminBranch, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	var out []domain.AdminBranch
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT b.id, b.project_id, b.org_id, b.parent_id, b.name, b.role, b.state,
			       b.compute_min_cu, b.compute_max_cu, COALESCE(b.current_cu, 0),
			       b.suspend_timeout_s, b.retention_days, b.bootstrap_at, b.created_at,
			       o.name, p.name
			  FROM branches b
			  JOIN orgs o ON o.id = b.org_id
			  JOIN projects p ON p.id = b.project_id
			 WHERE b.state <> 'deleting' AND ($1 = '' OR b.state = $1)
			 ORDER BY b.created_at DESC, b.id DESC LIMIT $2`, state, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ab domain.AdminBranch
			if err := rows.Scan(&ab.ID, &ab.ProjectID, &ab.Branch.OrgID, &ab.ParentID, &ab.Name,
				&ab.Role, &ab.State, &ab.Compute.MinCU, &ab.Compute.MaxCU, &ab.Compute.CurrentCU,
				&ab.Compute.SuspendTimeoutS, &ab.RetentionDays, &ab.BootstrapAt, &ab.CreatedAt,
				&ab.OrgName, &ab.ProjectName); err != nil {
				return err
			}
			ab.OrgID = ab.Branch.OrgID
			out = append(out, ab)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) AdminRecentAudit(ctx context.Context, limit int) ([]domain.AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []domain.AuditEntry
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, org_id, actor_type, actor_id, action, target_type, target_id, ip, at, details
			   FROM audit_log ORDER BY id DESC LIMIT $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e domain.AuditEntry
			var details []byte
			if err := rows.Scan(&e.ID, &e.OrgID, &e.ActorType, &e.ActorID, &e.Action,
				&e.TargetType, &e.TargetID, &e.IP, &e.At, &details); err != nil {
				return err
			}
			if len(details) > 0 {
				_ = json.Unmarshal(details, &e.Details)
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) ResolveBranchOrg(ctx context.Context, branchID string) (string, error) {
	var orgID string
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT org_id FROM branches WHERE id = $1 AND state <> 'deleting'`, branchID).Scan(&orgID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return orgID, nil
}
