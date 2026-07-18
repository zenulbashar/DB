package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/ids"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

const branchCols = `id, project_id, org_id, parent_id, name, role, state,
	compute_min_cu, compute_max_cu, suspend_timeout_s, retention_days, created_at`

func scanBranch(row pgx.Row, b *domain.Branch) error {
	return row.Scan(&b.ID, &b.ProjectID, &b.OrgID, &b.ParentID, &b.Name, &b.Role, &b.State,
		&b.Compute.MinCU, &b.Compute.MaxCU, &b.Compute.SuspendTimeoutS, &b.RetentionDays, &b.CreatedAt)
}

// createBranchTx inserts a branch and its rw_direct + rw_pooled endpoint
// records inside an existing org-scoped transaction.
func createBranchTx(ctx context.Context, tx pgx.Tx, p store.CreateBranchParams) (*domain.Branch, error) {
	c := p.Compute
	if c.MinCU == 0 {
		c.MinCU = 0.25
	}
	if c.MaxCU == 0 {
		c.MaxCU = 2
	}
	if c.SuspendTimeoutS == 0 {
		c.SuspendTimeoutS = 300
	}
	b := domain.Branch{
		ID: ids.New(ids.Branch), ProjectID: p.ProjectID, OrgID: p.OrgID,
		ParentID: p.ParentID, Name: p.Name, Role: p.Role,
		State: domain.StateProvisioning, Compute: c, RetentionDays: 7,
		CreatedAt: time.Now().UTC(),
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO branches (id, project_id, org_id, parent_id, name, role, state,
		    compute_min_cu, compute_max_cu, suspend_timeout_s, retention_days, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		b.ID, b.ProjectID, b.OrgID, b.ParentID, b.Name, b.Role, b.State,
		b.Compute.MinCU, b.Compute.MaxCU, b.Compute.SuspendTimeoutS, b.RetentionDays, b.CreatedAt); err != nil {
		return nil, err
	}
	for _, kind := range []domain.EndpointKind{domain.EndpointRWDirect, domain.EndpointRWPooled} {
		ep := domain.Endpoint{
			ID: ids.New(ids.Endpoint), BranchID: b.ID, OrgID: p.OrgID, Kind: kind,
			State: domain.StateProvisioning, CreatedAt: b.CreatedAt,
		}
		ep.Host = domain.EndpointHost(ep.ID, p.Region)
		if _, err := tx.Exec(ctx,
			`INSERT INTO endpoints (id, branch_id, org_id, kind, host, state, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			ep.ID, ep.BranchID, ep.OrgID, ep.Kind, ep.Host, ep.State, ep.CreatedAt); err != nil {
			return nil, err
		}
		b.Endpoints = append(b.Endpoints, ep)
	}
	return &b, nil
}

func (s *Store) CreateBranch(ctx context.Context, p store.CreateBranchParams) (*domain.Branch, error) {
	var b *domain.Branch
	err := s.withOrgTx(ctx, p.OrgID, func(tx pgx.Tx) error {
		// Verify the project exists in this org and resolve the region + parent.
		var region string
		var defaultBranch *string
		err := tx.QueryRow(ctx,
			`SELECT region, default_branch_id FROM projects
			  WHERE id = $1 AND org_id = $2 AND state <> 'deleting'`,
			p.ProjectID, p.OrgID).Scan(&region, &defaultBranch)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		p.Region = region
		if p.ParentID == nil {
			p.ParentID = defaultBranch
		} else {
			var ok bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM branches WHERE id = $1 AND project_id = $2 AND state <> 'deleting')`,
				*p.ParentID, p.ProjectID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return store.ErrNotFound
			}
		}
		b, err = createBranchTx(ctx, tx, p)
		if isUnique(err) {
			return store.ErrConflict
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Store) GetBranch(ctx context.Context, orgID, branchID string) (*domain.Branch, error) {
	var b domain.Branch
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		if err := scanBranch(tx.QueryRow(ctx,
			`SELECT `+branchCols+` FROM branches
			  WHERE id = $1 AND org_id = $2 AND state <> 'deleting'`, branchID, orgID), &b); err != nil {
			return err
		}
		eps, err := listEndpointsTx(ctx, tx, branchID)
		if err != nil {
			return err
		}
		b.Endpoints = eps
		return nil
	})
	return orNotFound(&b, err)
}

func (s *Store) ListBranches(ctx context.Context, orgID, projectID string, pg store.Page) ([]domain.Branch, string, error) {
	limit := normalizeLimit(pg.Limit)
	var out []domain.Branch
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		// Distinguish "project unknown" from "no branches".
		var ok bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1 AND org_id = $2 AND state <> 'deleting')`,
			projectID, orgID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return store.ErrNotFound
		}
		rows, err := tx.Query(ctx,
			`SELECT `+branchCols+` FROM branches
			  WHERE project_id = $1 AND state <> 'deleting' AND ($2 = '' OR id < $2)
			  ORDER BY id DESC LIMIT $3`, projectID, pg.Cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b domain.Branch
			if err := scanBranch(rows, &b); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	return trimPage(out, limit, func(b domain.Branch) string { return b.ID })
}

func (s *Store) UpdateBranch(ctx context.Context, orgID, branchID string, p store.UpdateBranchParams) (*domain.Branch, error) {
	var b domain.Branch
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		err := scanBranch(tx.QueryRow(ctx,
			`UPDATE branches SET
			    name              = COALESCE($3, name),
			    compute_min_cu    = COALESCE($4, compute_min_cu),
			    compute_max_cu    = COALESCE($5, compute_max_cu),
			    suspend_timeout_s = COALESCE($6, suspend_timeout_s),
			    retention_days    = COALESCE($7, retention_days)
			  WHERE id = $1 AND org_id = $2 AND state <> 'deleting'
			  RETURNING `+branchCols, branchID, orgID,
			p.Name, p.MinCU, p.MaxCU, p.SuspendTimeoutS, p.RetentionDays), &b)
		if isUnique(err) {
			return store.ErrConflict
		}
		return err
	})
	return orNotFound(&b, err)
}

func (s *Store) SoftDeleteBranch(ctx context.Context, orgID, branchID string, at time.Time) error {
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		var isDefault bool
		err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM projects p
			   JOIN branches b ON b.project_id = p.id
			  WHERE b.id = $1 AND b.org_id = $2 AND p.default_branch_id = b.id)`,
			branchID, orgID).Scan(&isDefault)
		if err != nil {
			return err
		}
		if isDefault {
			return store.ErrDefaultBranch
		}
		ct, err := tx.Exec(ctx,
			`UPDATE branches SET state = 'deleting', deleted_at = $3
			  WHERE id = $1 AND org_id = $2 AND state <> 'deleting'`, branchID, orgID, at)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		_, err = tx.Exec(ctx,
			`UPDATE endpoints SET state = 'deleting' WHERE branch_id = $1`, branchID)
		return err
	})
}

func listEndpointsTx(ctx context.Context, tx pgx.Tx, branchID string) ([]domain.Endpoint, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, branch_id, org_id, kind, host, state, created_at
		   FROM endpoints WHERE branch_id = $1 ORDER BY kind`, branchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Endpoint
	for rows.Next() {
		var e domain.Endpoint
		if err := rows.Scan(&e.ID, &e.BranchID, &e.OrgID, &e.Kind, &e.Host, &e.State, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListEndpoints(ctx context.Context, orgID, branchID string) ([]domain.Endpoint, error) {
	var out []domain.Endpoint
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM branches WHERE id = $1 AND org_id = $2 AND state <> 'deleting')`,
			branchID, orgID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return store.ErrNotFound
		}
		eps, err := listEndpointsTx(ctx, tx, branchID)
		if err != nil {
			return err
		}
		out = eps
		return nil
	})
	return out, err
}
