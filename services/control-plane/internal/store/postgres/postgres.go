// Package postgres is the production Store. Org scoping is enforced twice:
// by parameterized org_id predicates AND by Postgres RLS keyed on the
// per-transaction app.current_org setting (MULTI_TENANCY §2) — a bug in one
// layer is caught by the other.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/ids"
	"github.com/zenulbashar/DB/services/control-plane/internal/slug"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

const uniqueViolation = "23505"

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping control-plane db: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Pool() *pgxpool.Pool { return s.pool }
func (s *Store) Close()              { s.pool.Close() }

// RLSBypassed reports whether the connected role would bypass row-level
// security (superuser or BYPASSRLS). The control plane must never run as such
// a role — RLS is the second isolation net (MULTI_TENANCY §2) and silently
// losing it is a security regression, so main() refuses to start when true.
func RLSBypassed(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var bypass bool
	err := pool.QueryRow(ctx,
		`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&bypass)
	return bypass, err
}

// withOrgTx runs fn inside a transaction whose RLS context is pinned to orgID.
func (s *Store) withOrgTx(ctx context.Context, orgID string, fn func(pgx.Tx) error) error {
	return s.withTx(ctx, "app.current_org", orgID, fn)
}

// withPrivTx runs fn with the privileged RLS escape hatch. Keep the callers of
// this to the pre-org-resolution paths only (bootstrap, key lookup).
func (s *Store) withPrivTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return s.withTx(ctx, "app.privileged", "on", fn)
}

func (s *Store) withTx(ctx context.Context, setting, value string, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, setting, value); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- privileged ---

func (s *Store) Bootstrapped(ctx context.Context) (bool, error) {
	var exists bool
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orgs)`).Scan(&exists)
	})
	return exists, err
}

func (s *Store) Bootstrap(ctx context.Context, p store.BootstrapParams) (*store.BootstrapResult, error) {
	var res store.BootstrapResult
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		// Serialize competing bootstrap attempts.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(823472)`); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orgs)`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return store.ErrAlreadyBoot
		}
		now := time.Now().UTC()
		org := domain.Org{ID: ids.New(ids.Org), Name: p.OrgName, Slug: slug.Make(p.OrgName), Plan: "free", CreatedAt: now}
		if _, err := tx.Exec(ctx,
			`INSERT INTO orgs (id, name, slug, plan, created_at) VALUES ($1,$2,$3,$4,$5)`,
			org.ID, org.Name, org.Slug, org.Plan, org.CreatedAt); err != nil {
			return err
		}
		user := domain.User{ID: ids.New(ids.User), Email: p.Email, Name: p.Name, CreatedAt: now}
		if _, err := tx.Exec(ctx,
			`INSERT INTO users (id, email, name, created_at) VALUES ($1, lower($2), $3, $4)`,
			user.ID, user.Email, user.Name, user.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO org_members (org_id, user_id, role, added_at) VALUES ($1,$2,'owner',$3)`,
			org.ID, user.ID, now); err != nil {
			return err
		}
		key := domain.APIKey{
			ID: ids.New(ids.APIKey), OrgID: org.ID, Name: p.KeyName, Prefix: p.KeyPrefix,
			Scopes: p.Scopes, CreatedAt: now,
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO api_keys (id, org_id, name, key_hash, prefix, scopes, created_by, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			key.ID, key.OrgID, key.Name, p.KeyHash, key.Prefix, scopeStrings(key.Scopes), user.ID, now); err != nil {
			return err
		}
		res = store.BootstrapResult{Org: org, User: user, APIKey: key}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

func (s *Store) FindAPIKeyByHash(ctx context.Context, hash string) (*store.APIKeyAuth, error) {
	var k domain.APIKey
	var scopes []string
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, org_id, name, prefix, scopes, last_used_at, expires_at, revoked_at, created_at
			   FROM api_keys
			  WHERE key_hash = $1
			    AND revoked_at IS NULL
			    AND (expires_at IS NULL OR expires_at > now())`, hash).
			Scan(&k.ID, &k.OrgID, &k.Name, &k.Prefix, &scopes, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt, &k.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrKeyInvalid
	}
	if err != nil {
		return nil, err
	}
	k.Scopes = toScopes(scopes)
	return &store.APIKeyAuth{Key: k}, nil
}

func (s *Store) TouchAPIKey(ctx context.Context, keyID string, at time.Time) error {
	return s.withPrivTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE api_keys SET last_used_at = $2 WHERE id = $1`, keyID, at)
		return err
	})
}

// --- org-scoped ---

func (s *Store) GetOrg(ctx context.Context, orgID string) (*domain.Org, error) {
	var o domain.Org
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, name, slug, plan, created_at FROM orgs WHERE id = $1`, orgID).
			Scan(&o.ID, &o.Name, &o.Slug, &o.Plan, &o.CreatedAt)
	})
	return orNotFound(&o, err)
}

func (s *Store) UpdateOrgName(ctx context.Context, orgID, name string) (*domain.Org, error) {
	var o domain.Org
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`UPDATE orgs SET name = $2, updated_at = now() WHERE id = $1
			 RETURNING id, name, slug, plan, created_at`, orgID, name).
			Scan(&o.ID, &o.Name, &o.Slug, &o.Plan, &o.CreatedAt)
	})
	return orNotFound(&o, err)
}

func (s *Store) ListMembers(ctx context.Context, orgID string) ([]domain.Member, error) {
	var out []domain.Member
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT m.org_id, m.role, m.added_at, u.id, u.email, u.name, u.created_at
			   FROM org_members m JOIN users u ON u.id = m.user_id
			  WHERE m.org_id = $1 ORDER BY u.id`, orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m domain.Member
			if err := rows.Scan(&m.OrgID, &m.Role, &m.AddedAt, &m.User.ID, &m.User.Email, &m.User.Name, &m.User.CreatedAt); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) AddMember(ctx context.Context, orgID, email string, name *string, role domain.OrgRole) (*domain.Member, error) {
	var m domain.Member
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireOrg(ctx, tx, orgID); err != nil {
			return err
		}
		now := time.Now().UTC()
		var u domain.User
		// users is global (no RLS): find-or-create by email.
		err := tx.QueryRow(ctx, `SELECT id, email, name, created_at FROM users WHERE lower(email) = lower($1)`, email).
			Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			u = domain.User{ID: ids.New(ids.User), Email: email, Name: name, CreatedAt: now}
			if _, err := tx.Exec(ctx,
				`INSERT INTO users (id, email, name, created_at) VALUES ($1, lower($2), $3, $4)`,
				u.ID, u.Email, u.Name, u.CreatedAt); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO org_members (org_id, user_id, role, added_at) VALUES ($1,$2,$3,$4)`,
			orgID, u.ID, role, now); err != nil {
			if isUnique(err) {
				return store.ErrConflict
			}
			return err
		}
		m = domain.Member{OrgID: orgID, User: u, Role: role, AddedAt: now}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Store) UpdateMemberRole(ctx context.Context, orgID, userID string, role domain.OrgRole) (*domain.Member, error) {
	var m domain.Member
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		cur, err := memberRole(ctx, tx, orgID, userID)
		if err != nil {
			return err
		}
		if cur == domain.RoleOwner && role != domain.RoleOwner {
			if err := requireAnotherOwner(ctx, tx, orgID, userID); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx,
			`UPDATE org_members m SET role = $3
			  FROM users u
			 WHERE m.org_id = $1 AND m.user_id = $2 AND u.id = m.user_id
			 RETURNING m.org_id, m.role, m.added_at, u.id, u.email, u.name, u.created_at`,
			orgID, userID, role).
			Scan(&m.OrgID, &m.Role, &m.AddedAt, &m.User.ID, &m.User.Email, &m.User.Name, &m.User.CreatedAt)
	})
	if err != nil {
		return nil, notFoundIfNoRows(err)
	}
	return &m, nil
}

func (s *Store) RemoveMember(ctx context.Context, orgID, userID string) error {
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		cur, err := memberRole(ctx, tx, orgID, userID)
		if err != nil {
			return err
		}
		if cur == domain.RoleOwner {
			if err := requireAnotherOwner(ctx, tx, orgID, userID); err != nil {
				return err
			}
		}
		ct, err := tx.Exec(ctx, `DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`, orgID, userID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (s *Store) CreateAPIKey(ctx context.Context, p store.CreateAPIKeyParams) (*domain.APIKey, error) {
	k := domain.APIKey{
		ID: ids.New(ids.APIKey), OrgID: p.OrgID, Name: p.Name, Prefix: p.Prefix,
		Scopes: p.Scopes, ExpiresAt: p.ExpiresAt, CreatedAt: time.Now().UTC(),
	}
	err := s.withOrgTx(ctx, p.OrgID, func(tx pgx.Tx) error {
		if err := requireOrg(ctx, tx, p.OrgID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO api_keys (id, org_id, name, key_hash, prefix, scopes, created_by, expires_at, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			k.ID, k.OrgID, k.Name, p.Hash, k.Prefix, scopeStrings(k.Scopes), p.CreatedBy, k.ExpiresAt, k.CreatedAt)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &k, nil
}

func (s *Store) ListAPIKeys(ctx context.Context, orgID string) ([]domain.APIKey, error) {
	var out []domain.APIKey
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, org_id, name, prefix, scopes, last_used_at, expires_at, revoked_at, created_at
			   FROM api_keys WHERE org_id = $1 ORDER BY id DESC`, orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k domain.APIKey
			var scopes []string
			if err := rows.Scan(&k.ID, &k.OrgID, &k.Name, &k.Prefix, &scopes, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt, &k.CreatedAt); err != nil {
				return err
			}
			k.Scopes = toScopes(scopes)
			out = append(out, k)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) RevokeAPIKey(ctx context.Context, orgID, keyID string, at time.Time) error {
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE api_keys SET revoked_at = COALESCE(revoked_at, $3)
			  WHERE id = $1 AND org_id = $2`, keyID, orgID, at)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (s *Store) CreateProject(ctx context.Context, p store.CreateProjectParams) (*domain.Project, error) {
	var pr domain.Project
	insert := func(tx pgx.Tx, slugValue string) error {
		pr = domain.Project{
			ID: ids.New(ids.Project), OrgID: p.OrgID, Name: p.Name,
			Slug: slugValue, Region: p.Region, PGVersion: p.PGVersion,
			State: domain.ProjectPending, CreatedAt: time.Now().UTC(),
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO projects (id, org_id, name, slug, region, pg_version, state, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			pr.ID, pr.OrgID, pr.Name, pr.Slug, pr.Region, pr.PGVersion, pr.State, pr.CreatedAt); err != nil {
			return err
		}
		// Default branch "main" (production) + its endpoints, atomically with
		// the project (store contract).
		b, err := createBranchTx(ctx, tx, store.CreateBranchParams{
			OrgID: p.OrgID, ProjectID: pr.ID, Name: "main",
			Role: domain.BranchProduction, Region: p.Region,
		})
		if err != nil {
			return err
		}
		pr.DefaultBranchID = &b.ID
		if _, err := tx.Exec(ctx,
			`UPDATE projects SET default_branch_id = $2 WHERE id = $1`, pr.ID, b.ID); err != nil {
			return err
		}
		if p.Seed != nil {
			role, err := createDBRoleTx(ctx, tx, store.CreateDBRoleParams{
				OrgID: p.OrgID, BranchID: b.ID, Name: p.Seed.RoleName, Secret: p.Seed.Secret,
			})
			if err != nil {
				return err
			}
			if _, err := createDatabaseTx(ctx, tx, p.OrgID, b.ID, p.Seed.DatabaseName, role.Name); err != nil {
				return err
			}
		}
		return nil
	}
	base := slug.Make(p.Name)

	// First attempt with the plain slug. A unique violation aborts the whole
	// transaction, so the suffix retry needs a fresh one.
	err := s.withOrgTx(ctx, p.OrgID, func(tx pgx.Tx) error {
		if err := requireOrg(ctx, tx, p.OrgID); err != nil {
			return err
		}
		return insert(tx, base)
	})
	if err != nil && isUnique(err) {
		// Collision: pick the next free suffix by counting existing slugs.
		// A concurrent create can still race this to a second violation, which
		// surfaces as 409 for the caller to retry — acceptable for a
		// same-name-same-instant edge case.
		err = s.withOrgTx(ctx, p.OrgID, func(tx pgx.Tx) error {
			var n int
			if err := tx.QueryRow(ctx,
				`SELECT count(*) FROM projects WHERE org_id = $1 AND slug LIKE $2 || '%' AND state <> 'deleting'`,
				p.OrgID, base).Scan(&n); err != nil {
				return err
			}
			return insert(tx, slug.WithSuffix(base, n))
		})
	}
	if err != nil {
		if isUnique(err) {
			return nil, store.ErrConflict
		}
		return nil, err
	}
	return &pr, nil
}

const projectCols = `id, org_id, name, slug, region, pg_version, state, default_branch_id, created_at`

func (s *Store) GetProject(ctx context.Context, orgID, projectID string) (*domain.Project, error) {
	var pr domain.Project
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		return scanProject(tx.QueryRow(ctx,
			`SELECT `+projectCols+`
			   FROM projects WHERE id = $1 AND org_id = $2 AND state <> 'deleting'`,
			projectID, orgID), &pr)
	})
	return orNotFound(&pr, err)
}

func (s *Store) ListProjects(ctx context.Context, orgID string, pg store.Page) ([]domain.Project, string, error) {
	limit := normalizeLimit(pg.Limit)
	var out []domain.Project
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+projectCols+`
			   FROM projects
			  WHERE org_id = $1 AND state <> 'deleting' AND ($2 = '' OR id < $2)
			  ORDER BY id DESC LIMIT $3`, orgID, pg.Cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pr domain.Project
			if err := scanProject(rows, &pr); err != nil {
				return err
			}
			out = append(out, pr)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	return trimPage(out, limit, func(p domain.Project) string { return p.ID })
}

func (s *Store) UpdateProjectName(ctx context.Context, orgID, projectID, name string) (*domain.Project, error) {
	var pr domain.Project
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		return scanProject(tx.QueryRow(ctx,
			`UPDATE projects SET name = $3 WHERE id = $1 AND org_id = $2 AND state <> 'deleting'
			 RETURNING `+projectCols,
			projectID, orgID, name), &pr)
	})
	return orNotFound(&pr, err)
}

func (s *Store) SoftDeleteProject(ctx context.Context, orgID, projectID string, at time.Time) error {
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE projects SET state = 'deleting', deleted_at = $3
			  WHERE id = $1 AND org_id = $2 AND state <> 'deleting'`, projectID, orgID, at)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return store.ErrNotFound
		}
		// Cascade the soft-delete to the project's branches and endpoints so
		// the reconciler tears them down (default branch included — project
		// deletion is the one path allowed to remove it).
		if _, err := tx.Exec(ctx,
			`UPDATE branches SET state = 'deleting', deleted_at = $2
			  WHERE project_id = $1 AND state <> 'deleting'`, projectID, at); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE endpoints SET state = 'deleting'
			  WHERE branch_id IN (SELECT id FROM branches WHERE project_id = $1)`, projectID)
		return err
	})
}

func (s *Store) AppendAudit(ctx context.Context, e domain.AuditEntry) error {
	var details []byte
	if e.Details != nil {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return err
		}
		details = b
	}
	return s.withOrgTx(ctx, e.OrgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO audit_log (id, org_id, actor_type, actor_id, action, target_type, target_id, ip, at, details)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			e.ID, e.OrgID, e.ActorType, e.ActorID, e.Action, e.TargetType, e.TargetID, e.IP, e.At, details)
		return err
	})
}

func (s *Store) ListAudit(ctx context.Context, orgID string, pg store.Page) ([]domain.AuditEntry, string, error) {
	limit := normalizeLimit(pg.Limit)
	var out []domain.AuditEntry
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, org_id, actor_type, actor_id, action, target_type, target_id, ip, at, details
			   FROM audit_log
			  WHERE org_id = $1 AND ($2 = '' OR id < $2)
			  ORDER BY id DESC LIMIT $3`, orgID, pg.Cursor, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e domain.AuditEntry
			var details []byte
			if err := rows.Scan(&e.ID, &e.OrgID, &e.ActorType, &e.ActorID, &e.Action, &e.TargetType, &e.TargetID, &e.IP, &e.At, &details); err != nil {
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
		return nil, "", err
	}
	return trimPage(out, limit, func(e domain.AuditEntry) string { return e.ID })
}

func (s *Store) GetIdempotent(ctx context.Context, orgID, route, key string) (*store.IdempotentResponse, error) {
	var resp store.IdempotentResponse
	err := s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status, body FROM idempotency_keys
			  WHERE org_id = $1 AND route = $2 AND idem_key = $3 AND expires_at > now()`,
			orgID, route, key).Scan(&resp.Status, &resp.Body)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *Store) PutIdempotent(ctx context.Context, orgID, route, key string, resp store.IdempotentResponse, expires time.Time) error {
	return s.withOrgTx(ctx, orgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO idempotency_keys (org_id, route, idem_key, status, body, expires_at)
			 VALUES ($1,$2,$3,$4,$5,$6)
			 ON CONFLICT (org_id, route, idem_key) DO NOTHING`,
			orgID, route, key, resp.Status, resp.Body, expires)
		return err
	})
}

// --- helpers ---

func requireOrg(ctx context.Context, tx pgx.Tx, orgID string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orgs WHERE id = $1)`, orgID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	return nil
}

func memberRole(ctx context.Context, tx pgx.Tx, orgID, userID string) (domain.OrgRole, error) {
	var role domain.OrgRole
	err := tx.QueryRow(ctx,
		`SELECT role FROM org_members WHERE org_id = $1 AND user_id = $2`, orgID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrNotFound
	}
	return role, err
}

func requireAnotherOwner(ctx context.Context, tx pgx.Tx, orgID, exceptUserID string) error {
	var n int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM org_members WHERE org_id = $1 AND role = 'owner' AND user_id <> $2`,
		orgID, exceptUserID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return store.ErrLastOwner
	}
	return nil
}

func scanProject(row pgx.Row, pr *domain.Project) error {
	return row.Scan(&pr.ID, &pr.OrgID, &pr.Name, &pr.Slug, &pr.Region, &pr.PGVersion, &pr.State, &pr.DefaultBranchID, &pr.CreatedAt)
}

func scopeStrings(in []domain.Scope) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

func toScopes(in []string) []domain.Scope {
	out := make([]domain.Scope, len(in))
	for i, s := range in {
		out[i] = domain.Scope(s)
	}
	return out
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

func orNotFound[T any](v *T, err error) (*T, error) {
	if err != nil {
		return nil, notFoundIfNoRows(err)
	}
	return v, nil
}

func notFoundIfNoRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

func normalizeLimit(l int) int {
	if l <= 0 || l > 100 {
		return 25
	}
	return l
}

// trimPage converts a limit+1 result set into (page, next_cursor).
func trimPage[T any](all []T, limit int, id func(T) string) ([]T, string, error) {
	if len(all) > limit {
		page := all[:limit]
		return page, id(page[len(page)-1]), nil
	}
	return all, "", nil
}
