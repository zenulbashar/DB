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
	"strings"
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
		// A previous run (possibly as another role) may have left objects the
		// test role can't touch; recreate the schema so migrations run as
		// ndb_test and it owns everything (FORCE RLS applies to owners).
		// Schema-level reset is migration-proof — no table list to maintain.
		`DROP SCHEMA public CASCADE`,
		`CREATE SCHEMA public`,
		`GRANT ALL ON SCHEMA public TO ndb_test`,
		`GRANT ALL ON SCHEMA public TO public`,
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
	// The table list is derived from the catalog so new migrations are
	// covered automatically.
	err = s.withPrivTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'schema_migrations'`)
		if err != nil {
			return err
		}
		var tables []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			tables = append(tables, pgIdent(name))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(tables) == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `TRUNCATE `+strings.Join(tables, ", ")+` CASCADE`)
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

func TestSuspendResumeFlow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	pr, err := s.CreateProject(ctx, store.CreateProjectParams{OrgID: res.Org.ID, Name: "App", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}
	brID := *pr.DefaultBranchID
	if err := s.MarkBranchReady(ctx, brID); err != nil {
		t.Fatal(err)
	}

	// Cannot suspend anything but a ready branch: a fresh (already-ready) branch
	// suspends; endpoints follow.
	b, err := s.SuspendBranch(ctx, res.Org.ID, brID)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != domain.StateSuspending {
		t.Fatalf("branch state = %s, want suspending", b.State)
	}
	// The transition response must carry endpoints reflecting the new state —
	// identically to the memory store (audit finding on store divergence).
	if len(b.Endpoints) != 2 {
		t.Fatalf("suspend response endpoints = %d, want 2", len(b.Endpoints))
	}
	for _, ep := range b.Endpoints {
		if ep.State != domain.StateSuspending {
			t.Fatalf("endpoint %s state = %s, want suspending", ep.ID, ep.State)
		}
	}

	// The reconciler sees suspending as work, and the endpoints stay routable
	// (so the gateway can hold a connecting client) — mapped down elsewhere.
	work, _ := s.ListReconcileWork(ctx)
	if len(work) != 1 || work[0].Branch.State != domain.StateSuspending {
		t.Fatalf("suspend work = %+v", work)
	}
	if routable, _ := s.ListRoutableEndpoints(ctx); len(routable) != 2 {
		t.Fatalf("suspending endpoints must stay routable, got %d", len(routable))
	}

	// Idempotent suspend.
	if _, err := s.SuspendBranch(ctx, res.Org.ID, brID); err != nil {
		t.Fatalf("idempotent suspend: %v", err)
	}
	// Resume before suspended is a conflict.
	if _, err := s.ResumeBranch(ctx, res.Org.ID, brID); err != store.ErrConflict {
		t.Fatalf("resume from suspending = %v, want conflict", err)
	}

	// Reconciler completes hibernation.
	if err := s.MarkBranchSuspended(ctx, brID); err != nil {
		t.Fatal(err)
	}
	b, _ = s.GetBranch(ctx, res.Org.ID, brID)
	if b.State != domain.StateSuspended {
		t.Fatalf("branch state = %s, want suspended", b.State)
	}
	for _, ep := range b.Endpoints {
		if ep.State != domain.StateSuspended {
			t.Fatalf("endpoint state = %s, want suspended", ep.State)
		}
	}
	// A settled suspended branch is not reconcile work.
	if work, _ = s.ListReconcileWork(ctx); len(work) != 0 {
		t.Fatalf("suspended branch still in work queue: %+v", work)
	}

	// The branch is suspended; the privileged org-less gateway wake path resumes
	// it exactly like ResumeBranch — suspended → resuming (endpoints too).
	wb, err := s.WakeBranchByID(ctx, brID)
	if err != nil {
		t.Fatalf("wake by id: %v", err)
	}
	if wb.State != domain.StateResuming {
		t.Fatalf("wake-by-id state = %s, want resuming", wb.State)
	}
	for _, ep := range wb.Endpoints {
		if ep.State != domain.StateResuming {
			t.Fatalf("wake-by-id endpoint state = %s, want resuming", ep.State)
		}
	}
	// Idempotent (already resuming) and 404 for an unknown branch.
	if _, err := s.WakeBranchByID(ctx, brID); err != nil {
		t.Fatalf("idempotent wake: %v", err)
	}
	if _, err := s.WakeBranchByID(ctx, "br_doesnotexist"); err != store.ErrNotFound {
		t.Fatalf("wake unknown branch = %v, want not found", err)
	}

	// Resume → resuming → (reconciler) ready. (Branch is already resuming from
	// the wake above; MarkBranchResumed closes it out.)
	b, err = s.GetBranch(ctx, res.Org.ID, brID)
	if err != nil {
		t.Fatal(err)
	}
	if b.State != domain.StateResuming {
		t.Fatalf("branch state = %s, want resuming", b.State)
	}
	if work, _ = s.ListReconcileWork(ctx); len(work) != 1 || work[0].Branch.State != domain.StateResuming {
		t.Fatalf("resume work = %+v", work)
	}
	if err := s.MarkBranchResumed(ctx, brID); err != nil {
		t.Fatal(err)
	}
	b, _ = s.GetBranch(ctx, res.Org.ID, brID)
	if b.State != domain.StateReady {
		t.Fatalf("branch state = %s, want ready", b.State)
	}
	for _, ep := range b.Endpoints {
		if ep.State != domain.StateReady {
			t.Fatalf("endpoint state = %s, want ready", ep.State)
		}
	}

	// Cross-org suspend is 404, no mutation.
	if _, err := s.SuspendBranch(ctx, "org_other", brID); err != store.ErrNotFound {
		t.Fatalf("cross-org suspend = %v, want not found", err)
	}
}

func TestSuspendOnIdle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")
	pr, err := s.CreateProject(ctx, store.CreateProjectParams{OrgID: res.Org.ID, Name: "App", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}

	// Create a set of branches exercising every sweep case. Helper: make a ready
	// branch and return its id.
	ready := func(name string, timeout int, aged bool) string {
		br, err := s.CreateBranch(ctx, store.CreateBranchParams{
			OrgID: res.Org.ID, ProjectID: pr.ID, Name: name, Role: domain.BranchDevelopment,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.MarkBranchReady(ctx, br.ID); err != nil { // seeds last_active_at = now
			t.Fatal(err)
		}
		// Adjust the timeout and age the idle clock PRIVILEGED — a bare pool
		// connection runs under RLS with no org context, so these UPDATEs would
		// silently match zero rows.
		if err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `UPDATE branches SET suspend_timeout_s = $2 WHERE id = $1`, br.ID, timeout); e != nil {
				return e
			}
			if aged {
				if _, e := tx.Exec(ctx, `UPDATE branches SET last_active_at = now() - interval '1 hour' WHERE id = $1`, br.ID); e != nil {
					return e
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return br.ID
	}

	brIdle := ready("idle", 300, true)           // idle past timeout → suspend
	brGrace := ready("grace", 300, false)        // recently ready → grace, no suspend
	brActive := ready("active", 300, true)       // active on gw-1 → no suspend
	brElsewhere := ready("elsewhere", 300, true) // active on gw-2 only → no suspend
	brDisabled := ready("disabled", 0, true)     // autosuspend off → no suspend

	stale := 60 * time.Second

	// Fail-safe: with NO gateway reporting, an idle branch is NOT swept.
	if ids, err := s.SweepIdleBranches(ctx, stale); err != nil || len(ids) != 0 {
		t.Fatalf("sweep with no reports = %v (%v), want none (fail-safe)", ids, err)
	}

	// Reports arrive: gw-1 sees everyone (idle/grace/elsewhere/disabled at 0,
	// active at 5); gw-2 sees the 'elsewhere' branch busy. Reporting active>0
	// bumps last_active_at, so brActive/brElsewhere are no longer idle.
	if err := s.ReportGatewayActivity(ctx, "gw-1", map[string]int{
		brIdle: 0, brGrace: 0, brActive: 5, brElsewhere: 0, brDisabled: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReportGatewayActivity(ctx, "gw-2", map[string]int{brElsewhere: 3}); err != nil {
		t.Fatal(err)
	}

	ids, err := s.SweepIdleBranches(ctx, stale)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != brIdle {
		t.Fatalf("swept = %v, want only the idle branch %s", ids, brIdle)
	}
	// The idle branch is now suspending (endpoints too); the others stay ready.
	if b, _ := s.GetBranch(ctx, res.Org.ID, brIdle); b.State != domain.StateSuspending {
		t.Fatalf("idle branch state = %s, want suspending", b.State)
	}
	for _, id := range []string{brGrace, brActive, brElsewhere, brDisabled} {
		if b, _ := s.GetBranch(ctx, res.Org.ID, id); b.State != domain.StateReady {
			t.Fatalf("branch %s state = %s, want ready (must not be swept)", id, b.State)
		}
	}

	// Idempotent: a second sweep finds nothing new (the branch is now suspending,
	// not ready).
	if ids, err := s.SweepIdleBranches(ctx, stale); err != nil || len(ids) != 0 {
		t.Fatalf("second sweep = %v (%v), want none", ids, err)
	}
}

func TestRolesSecretsFlowAndRLS(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	seed := &store.ProjectSeed{
		RoleName: "app_owner", DatabaseName: "app",
		Secret: store.SecretMaterial{Ciphertext: []byte("sealed-1"), KeyVersion: 1},
	}
	pr, err := s.CreateProject(ctx, store.CreateProjectParams{
		OrgID: res.Org.ID, Name: "App", Region: "syd1", PGVersion: 17, Seed: seed,
	})
	if err != nil {
		t.Fatal(err)
	}
	brID := *pr.DefaultBranchID

	// Seeded role + database exist; secret ciphertext round-trips.
	roles, err := s.ListDBRoles(ctx, res.Org.ID, brID)
	if err != nil || len(roles) != 1 || roles[0].Name != "app_owner" {
		t.Fatalf("roles = %+v (%v)", roles, err)
	}
	ct, err := s.GetDBRoleSecret(ctx, res.Org.ID, brID, "app_owner")
	if err != nil || string(ct) != "sealed-1" {
		t.Fatalf("secret = %q (%v)", ct, err)
	}

	// Reset replaces ciphertext.
	if err := s.ResetDBRolePassword(ctx, res.Org.ID, brID, "app_owner",
		store.SecretMaterial{Ciphertext: []byte("sealed-2"), KeyVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if ct, _ = s.GetDBRoleSecret(ctx, res.Org.ID, brID, "app_owner"); string(ct) != "sealed-2" {
		t.Fatalf("secret after reset = %q", ct)
	}

	// Owner-of-database deletion protection.
	if err := s.DeleteDBRole(ctx, res.Org.ID, brID, "app_owner"); err != store.ErrConflict {
		t.Fatalf("delete owning role err = %v, want ErrConflict", err)
	}

	// RLS: a foreign org context sees no secrets/roles even unscoped.
	otherOrg := ids.New(ids.Org)
	err = s.withPrivTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO orgs (id, name, slug) VALUES ($1, 'Other', 'other-sec')`, otherOrg)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var leakedSecrets, leakedRoles int
	err = s.withOrgTx(ctx, otherOrg, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM secrets`).Scan(&leakedSecrets); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM db_roles`).Scan(&leakedRoles)
	})
	if err != nil {
		t.Fatal(err)
	}
	if leakedSecrets != 0 || leakedRoles != 0 {
		t.Fatalf("RLS leak: %d secrets, %d roles visible cross-org", leakedSecrets, leakedRoles)
	}

	// Cross-org access via the API paths is a 404-shaped ErrNotFound.
	if _, err := s.GetDBRoleSecret(ctx, otherOrg, brID, "app_owner"); err != store.ErrNotFound {
		t.Fatalf("cross-org secret read err = %v, want ErrNotFound", err)
	}
}

// Regression for the audit finding that FinishBranchTeardown's DELETE raised
// a foreign-key violation (wedging the reconciler forever) once an import row
// referenced the branch, and that it orphaned the roles' secrets.
func TestBranchTeardownSurvivesReferencesAndCleansSecrets(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	// Project with a seeded owner role + database (=> a secret row) on main.
	seed := &store.ProjectSeed{
		RoleName: "app_owner", DatabaseName: "app",
		Secret: store.SecretMaterial{Ciphertext: []byte("sealed"), KeyVersion: 1},
	}
	pr, err := s.CreateProject(ctx, store.CreateProjectParams{
		OrgID: res.Org.ID, Name: "App", Region: "syd1", PGVersion: 17, Seed: seed,
	})
	if err != nil {
		t.Fatal(err)
	}
	mainBranch := *pr.DefaultBranchID

	// An import targeting the branch — the FK that used to block the delete.
	if _, err := s.CreateImport(ctx, store.CreateImportParams{
		OrgID: res.Org.ID, ProjectID: pr.ID, SourceKind: domain.ImportNeon,
		Mode:         domain.ImportDumpRestore,
		SourceSecret: store.SecretMaterial{Ciphertext: []byte("src"), KeyVersion: 1},
	}); err != nil {
		t.Fatal(err)
	}

	var secretsBefore int
	if err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM secrets`).Scan(&secretsBefore)
	}); err != nil {
		t.Fatal(err)
	}
	if secretsBefore == 0 {
		t.Fatal("expected the seeded role secret to exist")
	}

	// Soft-delete then finish teardown — must NOT raise 23503.
	if err := s.SoftDeleteProject(ctx, res.Org.ID, pr.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishBranchTeardown(ctx, mainBranch); err != nil {
		t.Fatalf("teardown wedged on a referencing row: %v", err)
	}

	// Branch gone; import's target_branch_id nulled; role secret cleaned up.
	if _, err := s.GetBranch(ctx, res.Org.ID, mainBranch); err != store.ErrNotFound {
		t.Fatalf("branch survived teardown: %v", err)
	}
	if err := s.withPrivTx(ctx, func(tx pgx.Tx) error {
		var dangling int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM imports WHERE target_branch_id = $1`, mainBranch).Scan(&dangling); err != nil {
			return err
		}
		if dangling != 0 {
			t.Errorf("import still references the deleted branch (%d)", dangling)
		}
		var roleSecrets int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM secrets WHERE kind = 'db_password'`).Scan(&roleSecrets); err != nil {
			return err
		}
		if roleSecrets != 0 {
			t.Errorf("role secret orphaned after teardown (%d remain)", roleSecrets)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The slug gap-filling loop must match the memory store: three same-name
// projects yield three distinct slugs, not a 409 (audit: store parity).
func TestSlugCollisionFillsGaps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")
	slugs := map[string]bool{}
	for i := 0; i < 3; i++ {
		pr, err := s.CreateProject(ctx, store.CreateProjectParams{OrgID: res.Org.ID, Name: "Same Name", Region: "syd1", PGVersion: 17})
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		if slugs[pr.Slug] {
			t.Fatalf("duplicate slug %q", pr.Slug)
		}
		slugs[pr.Slug] = true
	}
}

// Regression for the CRITICAL audit finding: the claim released its lock at
// commit, so a second worker re-claimed the same import mid-stage and drove a
// concurrent, corrupting dump/restore. The lease must give one worker
// exclusive ownership across the whole stage, and expire so a crashed worker's
// import can be resumed.
func TestImportClaimLease(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")
	pr, err := s.CreateProject(ctx, store.CreateProjectParams{OrgID: res.Org.ID, Name: "App", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateImport(ctx, store.CreateImportParams{
		OrgID: res.Org.ID, ProjectID: pr.ID, SourceKind: domain.ImportNeon, Mode: domain.ImportDumpRestore,
		SourceSecret: store.SecretMaterial{Ciphertext: []byte("ct"), KeyVersion: 1},
	}); err != nil {
		t.Fatal(err)
	}

	// Worker A claims and holds the lease.
	a, err := s.ClaimActionableImport(ctx, "worker-A", 30*time.Minute)
	if err != nil || a == nil {
		t.Fatalf("A claim: %v (%v)", a, err)
	}
	// Worker B, polling mid-stage, must NOT get the same import.
	b, err := s.ClaimActionableImport(ctx, "worker-B", 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if b != nil {
		t.Fatalf("worker B double-claimed import %s while A holds the lease", b.ImportID)
	}
	// Worker A may re-claim its own import (to drive the next stage).
	again, err := s.ClaimActionableImport(ctx, "worker-A", 30*time.Minute)
	if err != nil || again == nil || again.ImportID != a.ImportID {
		t.Fatalf("A re-claim: %v (%v)", again, err)
	}

	// With a zero TTL the lease is always expired, so B can take over a
	// crashed worker's import.
	takeover, err := s.ClaimActionableImport(ctx, "worker-B", 0)
	if err != nil || takeover == nil || takeover.ImportID != a.ImportID {
		t.Fatalf("B takeover after lease expiry: %v (%v)", takeover, err)
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
