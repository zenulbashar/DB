//go:build integration

// Integration tests for the Postgres store. They need a scratch database:
//
//	TEST_DATABASE_URL=postgres://…/nimbusdb_test go test -tags=integration ./internal/store/postgres/
//
// CI provides a postgres:17 service container. Tables are truncated between
// tests; never point this at a real environment.
package postgres

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/ids"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

// ensureAppRole provisions the non-superuser test role and returns a
// connection URL for it against the same database.
func ensureAppRole(t *testing.T, ctx context.Context, adminURL string) string {
	t.Helper()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	_, _ = admin.Exec(ctx, `CREATE ROLE ndb_test LOGIN PASSWORD 'ndb_test' NOSUPERUSER NOBYPASSRLS`)
	var dbName string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`GRANT CONNECT, TEMP ON DATABASE ` + pgIdent(dbName) + ` TO ndb_test`,
		`GRANT ALL ON SCHEMA public TO ndb_test`,
		// A previous run (possibly as another role) may have left objects the
		// test role can't touch; start from a clean slate so migrations run
		// as ndb_test and it owns everything (FORCE RLS applies to owners).
		`DROP TABLE IF EXISTS schema_migrations, orgs, users, org_members,
		     api_keys, projects, branches, endpoints, audit_log, idempotency_keys CASCADE`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("ndb_test", "ndb_test")
	return u.String()
}

// pgIdent quotes an identifier defensively (test-only helper).
func pgIdent(name string) string {
	return `"` + name + `"`
}

func testStore(t *testing.T) *Store {
	t.Helper()
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration tests")
	}
	ctx := context.Background()

	// The store under test must run as a NON-superuser role or RLS is
	// silently bypassed (superusers skip policies). Use the provided URL —
	// typically a superuser in CI — only to provision that role.
	appURL := ensureAppRole(t, ctx, adminURL)

	s, err := New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	if bypass, err := RLSBypassed(ctx, s.pool); err != nil {
		t.Fatal(err)
	} else if bypass {
		t.Fatal("test role bypasses RLS; integration tests would be meaningless")
	}

	if err := Migrate(ctx, s.pool); err != nil {
		t.Fatal(err)
	}
	// Reset state between tests (privileged: RLS would hide other orgs' rows).
	err = s.withPrivTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `TRUNCATE orgs, users, org_members, api_keys, projects, branches, endpoints, audit_log, idempotency_keys CASCADE`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func bootstrapOrg(t *testing.T, s *Store, orgName string) *store.BootstrapResult {
	t.Helper()
	res, err := s.Bootstrap(context.Background(), store.BootstrapParams{
		Email: orgName + "@example.com", OrgName: orgName,
		KeyName: "owner", KeyHash: "hash-" + orgName, KeyPrefix: "ndb_test",
		Scopes: domain.AllScopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := testStore(t)
	if err := Migrate(context.Background(), s.pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestBootstrapOnce(t *testing.T) {
	s := testStore(t)
	bootstrapOrg(t, s, "acme")
	_, err := s.Bootstrap(context.Background(), store.BootstrapParams{
		Email: "b@example.com", OrgName: "other",
		KeyName: "k", KeyHash: "h2", KeyPrefix: "p",
		Scopes: domain.AllScopes,
	})
	if err != store.ErrAlreadyBoot {
		t.Fatalf("err = %v, want ErrAlreadyBoot", err)
	}
}

func TestRLSBlocksCrossOrgReads(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	// Create a second org directly (privileged), as the reconciler/console will.
	otherOrg := ids.New(ids.Org)
	err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO orgs (id, name, slug) VALUES ($1, 'Other', 'other')`, otherOrg)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateProject(ctx, store.CreateProjectParams{OrgID: otherOrg, Name: "secret", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}

	// Org A must not see org B's project — via the API path...
	projects, _, err := s.ListProjects(ctx, res.Org.ID, store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("org A sees %d foreign projects", len(projects))
	}

	// ...and even a buggy raw query inside org A's RLS context returns nothing.
	var leaked int
	err = s.withOrgTx(ctx, res.Org.ID, func(tx pgx.Tx) error {
		// Deliberately missing the org_id predicate.
		return tx.QueryRow(ctx, `SELECT count(*) FROM projects`).Scan(&leaked)
	})
	if err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatalf("RLS leak: unscoped query in org A context returned %d rows", leaked)
	}
}

func TestAuditIsAppendOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	e := domain.AuditEntry{
		ID: ids.New(ids.Audit), OrgID: res.Org.ID, ActorType: domain.ActorSystem,
		ActorID: "test", Action: "x", TargetType: "y", TargetID: "z", At: time.Now().UTC(),
	}
	if err := s.AppendAudit(ctx, e); err != nil {
		t.Fatal(err)
	}
	// UPDATE and DELETE have no RLS policy → zero rows affected even in-org.
	err := s.withOrgTx(ctx, res.Org.ID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `UPDATE audit_log SET action = 'tampered' WHERE id = $1`, e.ID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() != 0 {
			t.Errorf("audit UPDATE affected %d rows, want 0", ct.RowsAffected())
		}
		ct, err = tx.Exec(ctx, `DELETE FROM audit_log WHERE id = $1`, e.ID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() != 0 {
			t.Errorf("audit DELETE affected %d rows, want 0", ct.RowsAffected())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProjectSlugCollision(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	p1, err := s.CreateProject(ctx, store.CreateProjectParams{OrgID: res.Org.ID, Name: "My App", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.CreateProject(ctx, store.CreateProjectParams{OrgID: res.Org.ID, Name: "My App", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}
	if p1.Slug == p2.Slug {
		t.Fatalf("slug collision: both %q", p1.Slug)
	}
}

func TestKeyLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	found, err := s.FindAPIKeyByHash(ctx, "hash-acme")
	if err != nil {
		t.Fatal(err)
	}
	if found.Key.OrgID != res.Org.ID {
		t.Fatal("wrong org on key lookup")
	}

	if err := s.RevokeAPIKey(ctx, res.Org.ID, found.Key.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FindAPIKeyByHash(ctx, "hash-acme"); err != store.ErrKeyInvalid {
		t.Fatalf("revoked key lookup err = %v, want ErrKeyInvalid", err)
	}
}

func TestBranchLifecycleAndRLS(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	// Project creation provisions main + two endpoints atomically.
	pr, err := s.CreateProject(ctx, store.CreateProjectParams{OrgID: res.Org.ID, Name: "App", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}
	if pr.DefaultBranchID == nil {
		t.Fatal("no default branch id")
	}
	main, err := s.GetBranch(ctx, res.Org.ID, *pr.DefaultBranchID)
	if err != nil {
		t.Fatal(err)
	}
	if main.Name != "main" || main.Role != domain.BranchProduction || len(main.Endpoints) != 2 {
		t.Fatalf("unexpected default branch: %+v", main)
	}

	// Default branch is delete-protected; project delete cascades.
	if err := s.SoftDeleteBranch(ctx, res.Org.ID, main.ID, time.Now().UTC()); err != store.ErrDefaultBranch {
		t.Fatalf("delete default err = %v, want ErrDefaultBranch", err)
	}

	// Branch create from main.
	br, err := s.CreateBranch(ctx, store.CreateBranchParams{
		OrgID: res.Org.ID, ProjectID: pr.ID, Name: "preview", Role: domain.BranchPreview,
	})
	if err != nil {
		t.Fatal(err)
	}
	if br.ParentID == nil || *br.ParentID != main.ID {
		t.Fatalf("parent = %v, want %s", br.ParentID, main.ID)
	}

	// RLS: another org's context must see zero branches/endpoints even with
	// an unscoped query.
	otherOrg := ids.New(ids.Org)
	err = s.withPrivTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO orgs (id, name, slug) VALUES ($1, 'Other', 'other-br')`, otherOrg)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var leakedBranches, leakedEndpoints int
	err = s.withOrgTx(ctx, otherOrg, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM branches`).Scan(&leakedBranches); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM endpoints`).Scan(&leakedEndpoints)
	})
	if err != nil {
		t.Fatal(err)
	}
	if leakedBranches != 0 || leakedEndpoints != 0 {
		t.Fatalf("RLS leak: %d branches, %d endpoints visible cross-org", leakedBranches, leakedEndpoints)
	}

	// Project soft-delete cascades states.
	if err := s.SoftDeleteProject(ctx, res.Org.ID, pr.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBranch(ctx, res.Org.ID, main.ID); err != store.ErrNotFound {
		t.Fatalf("main after project delete err = %v, want ErrNotFound", err)
	}
}

func TestReconcilerStoreFlow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	pr, err := s.CreateProject(ctx, store.CreateProjectParams{OrgID: res.Org.ID, Name: "App", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}

	// New project's main branch appears as provisioning work with context.
	work, err := s.ListReconcileWork(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 || work[0].Branch.ID != *pr.DefaultBranchID ||
		work[0].Region != "syd1" || work[0].PGVersion != 17 || len(work[0].Endpoints) != 2 {
		t.Fatalf("unexpected work: %+v", work)
	}

	// Nothing routable until ready.
	routable, err := s.ListRoutableEndpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routable) != 0 {
		t.Fatalf("provisioning endpoints must not be routable: %+v", routable)
	}

	if err := s.MarkBranchReady(ctx, *pr.DefaultBranchID); err != nil {
		t.Fatal(err)
	}
	if work, _ = s.ListReconcileWork(ctx); len(work) != 0 {
		t.Fatalf("ready branch still in work queue: %+v", work)
	}
	if routable, _ = s.ListRoutableEndpoints(ctx); len(routable) != 2 {
		t.Fatalf("want 2 routable endpoints, got %d", len(routable))
	}

	// Deleting the project queues teardown; finishing it clears the rows
	// (including the default-branch FK back-reference).
	if err := s.SoftDeleteProject(ctx, res.Org.ID, pr.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	work, _ = s.ListReconcileWork(ctx)
	if len(work) != 1 || work[0].Branch.State != domain.StateDeleting {
		t.Fatalf("teardown work = %+v", work)
	}
	n, err := s.CountLiveBranches(ctx, pr.ID)
	if err != nil || n != 0 {
		t.Fatalf("live branches = %d (%v), want 0", n, err)
	}
	if err := s.FinishBranchTeardown(ctx, *pr.DefaultBranchID); err != nil {
		t.Fatal(err)
	}
	if work, _ = s.ListReconcileWork(ctx); len(work) != 0 {
		t.Fatalf("work queue not drained: %+v", work)
	}
}

func TestLastOwnerRule(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	if _, err := s.UpdateMemberRole(ctx, res.Org.ID, res.User.ID, domain.RoleMember); err != store.ErrLastOwner {
		t.Fatalf("demote last owner err = %v, want ErrLastOwner", err)
	}
	m, err := s.AddMember(ctx, res.Org.ID, "second@example.com", nil, domain.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateMemberRole(ctx, res.Org.ID, res.User.ID, domain.RoleMember); err != nil {
		t.Fatalf("demote with second owner: %v", err)
	}
	if err := s.RemoveMember(ctx, res.Org.ID, m.User.ID); err != store.ErrLastOwner {
		t.Fatalf("remove new last owner err = %v, want ErrLastOwner", err)
	}
}
