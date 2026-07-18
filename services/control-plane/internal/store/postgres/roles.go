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

const SecretIDPrefix = "sec"

func requireBranch(ctx context.Context, tx pgx.Tx, orgID, branchID string) error {
	var ok bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM branches WHERE id = $1 AND org_id = $2 AND state <> 'deleting')`,
		branchID, orgID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	return nil
}

// createDBRoleTx inserts a secret + role row inside an org-scoped tx.
func createDBRoleTx(ctx context.Context, tx pgx.Tx, p store.CreateDBRoleParams) (*domain.DBRole, error) {
	now := time.Now().UTC()
	secretID := ids.New(SecretIDPrefix)
	if _, err := tx.Exec(ctx,
		`INSERT INTO secrets (id, org_id, kind, ciphertext, key_version, created_at)
		 VALUES ($1,$2,'db_password',$3,$4,$5)`,
		secretID, p.OrgID, p.Secret.Ciphertext, p.Secret.KeyVersion, now); err != nil {
		return nil, err
	}
	r := domain.DBRole{
		ID: ids.New("role"), BranchID: p.BranchID, OrgID: p.OrgID,
		Name: p.Name, SecretID: secretID, CreatedAt: now,
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO db_roles (id, branch_id, org_id, name, secret_id, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		r.ID, r.BranchID, r.OrgID, r.Name, r.SecretID, r.CreatedAt); err != nil {
		if isUnique(err) {
			return nil, store.ErrConflict
		}
		return nil, err
	}
	return &r, nil
}

func (s *Store) CreateDBRole(ctx context.Context, p store.CreateDBRoleParams) (*domain.DBRole, error) {
	var r *domain.DBRole
	err := s.withOrgTx(ctx, p.OrgID, func(tx pgx.Tx) error {
		if err := requireBranch(ctx, tx, p.OrgID, p.BranchID); err != nil {
			return err
		}
		var err error
		r, err = createDBRoleTx(ctx, tx, p)
		return err
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Store) ListDBRoles(ctx context.Context, orgID, branchID string) ([]domain.DBRole, error) {
	var out []domain.DBRole
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireBranch(ctx, tx, orgID, branchID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT id, branch_id, org_id, name, secret_id, created_at
			   FROM db_roles WHERE branch_id = $1 ORDER BY name`, branchID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.DBRole
			if err := rows.Scan(&r.ID, &r.BranchID, &r.OrgID, &r.Name, &r.SecretID, &r.CreatedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) DeleteDBRole(ctx context.Context, orgID, branchID, name string) error {
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		var owns bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM databases d JOIN db_roles r ON r.id = d.owner_role_id
			  WHERE r.branch_id = $1 AND r.name = $2)`, branchID, name).Scan(&owns); err != nil {
			return err
		}
		if owns {
			return store.ErrConflict
		}
		var secretID string
		err := tx.QueryRow(ctx,
			`DELETE FROM db_roles WHERE branch_id = $1 AND org_id = $2 AND name = $3 RETURNING secret_id`,
			branchID, orgID, name).Scan(&secretID)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM secrets WHERE id = $1`, secretID)
		return err
	})
}

func (s *Store) ResetDBRolePassword(ctx context.Context, orgID, branchID, name string, secret store.SecretMaterial) error {
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE secrets SET ciphertext = $4, key_version = $5, rotated_at = now()
			  WHERE id = (SELECT secret_id FROM db_roles WHERE branch_id = $1 AND org_id = $2 AND name = $3)`,
			branchID, orgID, name, secret.Ciphertext, secret.KeyVersion)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (s *Store) GetDBRoleSecret(ctx context.Context, orgID, branchID, name string) ([]byte, error) {
	var ciphertext []byte
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT s.ciphertext FROM secrets s
			   JOIN db_roles r ON r.secret_id = s.id
			  WHERE r.branch_id = $1 AND r.org_id = $2 AND r.name = $3`,
			branchID, orgID, name).Scan(&ciphertext)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return ciphertext, nil
}

func createDatabaseTx(ctx context.Context, tx pgx.Tx, orgID, branchID, name, ownerRoleName string) (*domain.Database, error) {
	var roleID string
	err := tx.QueryRow(ctx,
		`SELECT id FROM db_roles WHERE branch_id = $1 AND name = $2`, branchID, ownerRoleName).Scan(&roleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d := domain.Database{
		ID: ids.New("db"), BranchID: branchID, OrgID: orgID,
		Name: name, OwnerRoleID: roleID, CreatedAt: time.Now().UTC(),
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO databases (id, branch_id, org_id, name, owner_role_id, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		d.ID, d.BranchID, d.OrgID, d.Name, d.OwnerRoleID, d.CreatedAt); err != nil {
		if isUnique(err) {
			return nil, store.ErrConflict
		}
		return nil, err
	}
	return &d, nil
}

func (s *Store) CreateDatabase(ctx context.Context, orgID, branchID, name, ownerRoleName string) (*domain.Database, error) {
	var d *domain.Database
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireBranch(ctx, tx, orgID, branchID); err != nil {
			return err
		}
		var err error
		d, err = createDatabaseTx(ctx, tx, orgID, branchID, name, ownerRoleName)
		return err
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Store) ListDatabases(ctx context.Context, orgID, branchID string) ([]domain.Database, error) {
	var out []domain.Database
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireBranch(ctx, tx, orgID, branchID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT id, branch_id, org_id, name, owner_role_id, created_at
			   FROM databases WHERE branch_id = $1 ORDER BY name`, branchID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d domain.Database
			if err := rows.Scan(&d.ID, &d.BranchID, &d.OrgID, &d.Name, &d.OwnerRoleID, &d.CreatedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) DeleteDatabase(ctx context.Context, orgID, branchID, name string) error {
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`DELETE FROM databases WHERE branch_id = $1 AND org_id = $2 AND name = $3`,
			branchID, orgID, name)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}
