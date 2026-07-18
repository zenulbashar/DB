package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/ids"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

const importCols = `id, project_id, org_id, target_branch_id, source_kind, mode, state,
	source_secret_id, report, checkpoints, error, created_at, updated_at`

func scanImport(row pgx.Row, im *domain.Import) error {
	var report, checkpoints []byte
	if err := row.Scan(&im.ID, &im.ProjectID, &im.OrgID, &im.TargetBranchID, &im.SourceKind,
		&im.Mode, &im.State, &im.SourceSecretID, &report, &checkpoints, &im.Error,
		&im.CreatedAt, &im.UpdatedAt); err != nil {
		return err
	}
	if len(report) > 0 {
		_ = json.Unmarshal(report, &im.Report)
	}
	if len(checkpoints) > 0 {
		_ = json.Unmarshal(checkpoints, &im.Checkpoints)
	}
	return nil
}

func (s *Store) CreateImport(ctx context.Context, p store.CreateImportParams) (*domain.Import, error) {
	now := time.Now().UTC()
	im := domain.Import{
		ID: ids.New("imp"), ProjectID: p.ProjectID, OrgID: p.OrgID,
		TargetBranchID: p.TargetBranchID, SourceKind: p.SourceKind, Mode: p.Mode,
		State: domain.ImportPending, CreatedAt: now, UpdatedAt: now,
	}
	err := s.withOrgTx(ctx, p.OrgID, func(tx pgx.Tx) error {
		// Project must exist; explicit target branch must belong to it.
		var defaultBranch *string
		err := tx.QueryRow(ctx,
			`SELECT default_branch_id FROM projects WHERE id = $1 AND org_id = $2 AND state <> 'deleting'`,
			p.ProjectID, p.OrgID).Scan(&defaultBranch)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if im.TargetBranchID == nil {
			im.TargetBranchID = defaultBranch
		} else {
			var ok bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM branches WHERE id = $1 AND project_id = $2 AND state <> 'deleting')`,
				*im.TargetBranchID, p.ProjectID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return store.ErrNotFound
			}
		}

		secretID := ids.New(SecretIDPrefix)
		if _, err := tx.Exec(ctx,
			`INSERT INTO secrets (id, org_id, kind, ciphertext, key_version, created_at)
			 VALUES ($1,$2,'integration_token',$3,$4,$5)`,
			secretID, p.OrgID, p.SourceSecret.Ciphertext, p.SourceSecret.KeyVersion, now); err != nil {
			return err
		}
		im.SourceSecretID = secretID
		_, err = tx.Exec(ctx,
			`INSERT INTO imports (id, project_id, org_id, target_branch_id, source_kind, mode,
			    state, source_secret_id, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			im.ID, im.ProjectID, im.OrgID, im.TargetBranchID, im.SourceKind, im.Mode,
			im.State, im.SourceSecretID, im.CreatedAt, im.UpdatedAt)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &im, nil
}

func (s *Store) GetImport(ctx context.Context, orgID, importID string) (*domain.Import, error) {
	var im domain.Import
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		return scanImport(tx.QueryRow(ctx,
			`SELECT `+importCols+` FROM imports WHERE id = $1 AND org_id = $2`, importID, orgID), &im)
	})
	return orNotFound(&im, err)
}

func (s *Store) ListImports(ctx context.Context, orgID, projectID string, pg store.Page) ([]domain.Import, string, error) {
	limit := normalizeLimit(pg.Limit)
	var out []domain.Import
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
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
			`SELECT `+importCols+` FROM imports
			  WHERE project_id = $1 AND ($2 = '' OR id < $2)
			  ORDER BY id DESC LIMIT $3`, projectID, pg.Cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var im domain.Import
			if err := scanImport(rows, &im); err != nil {
				return err
			}
			out = append(out, im)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	return trimPage(out, limit, func(im domain.Import) string { return im.ID })
}

// ClaimActionableImport atomically LEASES the oldest actionable import to
// workerID and returns the context + encrypted source URL a worker needs to
// drive the next stage. Returns (nil, nil) when nothing is claimable.
// Privileged: the import worker is a platform component with direct DB access,
// so decrypted source credentials never traverse the tenant API
// (SECURITY_MODEL §5).
//
// A row is claimable when it is actionable (non-terminal, not the cutover_ready
// human gate) AND (unclaimed OR its lease expired OR it is already ours). The
// UPDATE stamps claimed_by/claimed_at in the same statement, so the claim
// SURVIVES the transaction commit — a second replica polling mid-stage sees a
// fresh foreign lease and skips it, preventing the concurrent double-drive the
// old FOR-UPDATE-only version allowed (audit finding). A crashed worker's lease
// expires after leaseTTL and another worker resumes the import.
func (s *Store) ClaimActionableImport(ctx context.Context, workerID string, leaseTTL time.Duration) (*store.ClaimedImport, error) {
	var c store.ClaimedImport
	found := false
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			WITH claimed AS (
			  UPDATE imports SET claimed_by = $1, claimed_at = now()
			   WHERE id = (
			     SELECT id FROM imports
			      WHERE state NOT IN ('cutover_ready','verified','failed','aborted')
			        AND (claimed_at IS NULL
			             OR claimed_at < now() - make_interval(secs => $2)
			             OR claimed_by = $1)
			      ORDER BY updated_at
			      FOR UPDATE SKIP LOCKED
			      LIMIT 1)
			  RETURNING id, project_id, org_id, target_branch_id, source_kind, mode, state, source_secret_id
			)
			SELECT c.id, c.project_id, c.org_id, c.target_branch_id, c.source_kind, c.mode, c.state,
			       s.ciphertext, s.key_version, p.region
			  FROM claimed c
			  JOIN secrets s ON s.id = c.source_secret_id
			  JOIN projects p ON p.id = c.project_id`,
			workerID, leaseTTL.Seconds()).Scan(
			&c.ImportID, &c.ProjectID, &c.OrgID, &c.TargetBranchID, &c.SourceKind, &c.Mode, &c.State,
			&c.SourceCiphertext, &c.SourceKeyVersion, &c.Region)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return nil, err
	}
	return &c, nil
}

// TransitionImportByID is the privileged (org-agnostic) transition used by the
// platform import worker, which operates across tenants by design.
func (s *Store) TransitionImportByID(ctx context.Context, importID string, p store.TransitionImportParams) (*domain.Import, error) {
	var im domain.Import
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		if err := scanImport(tx.QueryRow(ctx,
			`SELECT `+importCols+` FROM imports WHERE id = $1 FOR UPDATE`, importID), &im); err != nil {
			return err
		}
		return applyTransition(ctx, tx, &im, p)
	})
	return orNotFound(&im, err)
}

func (s *Store) TransitionImport(ctx context.Context, orgID, importID string, p store.TransitionImportParams) (*domain.Import, error) {
	var im domain.Import
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		// Lock the row so concurrent transitions serialize.
		if err := scanImport(tx.QueryRow(ctx,
			`SELECT `+importCols+` FROM imports WHERE id = $1 AND org_id = $2 FOR UPDATE`,
			importID, orgID), &im); err != nil {
			return err
		}
		return applyTransition(ctx, tx, &im, p)
	})
	return orNotFound(&im, err)
}

// applyTransition validates the state-machine step and persists it. The row
// must already be located and locked FOR UPDATE by the caller (so the WHERE
// needs only the id).
func applyTransition(ctx context.Context, tx pgx.Tx, im *domain.Import, p store.TransitionImportParams) error {
	if !domain.CanTransition(im.Mode, im.State, p.To) {
		return store.ErrConflict
	}
	im.State = p.To
	if p.Report != nil {
		im.Report = p.Report
	}
	if p.Checkpoints != nil {
		if im.Checkpoints == nil {
			im.Checkpoints = map[string]any{}
		}
		for k, v := range p.Checkpoints {
			im.Checkpoints[k] = v
		}
	}
	im.Error = p.Error
	im.UpdatedAt = time.Now().UTC()

	report, _ := json.Marshal(im.Report)
	checkpoints, _ := json.Marshal(im.Checkpoints)
	if im.Report == nil {
		report = nil
	}
	if im.Checkpoints == nil {
		checkpoints = nil
	}
	_, err := tx.Exec(ctx,
		`UPDATE imports SET state = $2, report = $3, checkpoints = $4, error = $5, updated_at = $6
		  WHERE id = $1`,
		im.ID, im.State, report, checkpoints, im.Error, im.UpdatedAt)
	return err
}
