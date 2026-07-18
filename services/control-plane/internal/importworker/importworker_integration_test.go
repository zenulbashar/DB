//go:build integration

// End-to-end: the store-backed import worker drives a real dump_restore
// migration to `verified` — claim a pending import from the control-plane
// database, decrypt its source credential with the keyring, and run the shared
// import runner between two live databases. This exercises the whole secure
// wiring (no credentials over the tenant API) that the standalone runner test
// could only fake.
package importworker_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zenulbashar/DB/services/import-engine/runner"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/importworker"
	"github.com/zenulbashar/DB/services/control-plane/internal/secrets"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

func keyring(t *testing.T) *secrets.Keyring {
	t.Helper()
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	kr, err := secrets.ParseKeyring("1:"+base64.StdEncoding.EncodeToString(k), "")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func binDir() string {
	if _, err := exec.LookPath("pg_dump"); err == nil {
		return ""
	}
	for _, d := range []string{"/usr/lib/postgresql/17/bin", "/usr/lib/postgresql/16/bin"} {
		if _, err := os.Stat(d + "/pg_dump"); err == nil {
			return d
		}
	}
	return ""
}

func TestWorkerDrivesRealMigrationThroughStore(t *testing.T) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	// A DEDICATED control-plane database, plus source/target app databases.
	// Isolation matters: the postgres store suite recreates its schema, so
	// sharing a database across parallel packages races (audit-style
	// test-infra correctness). The worker paths are privileged; RLS is
	// covered by the store's own suite.
	st, srcURL, tgtURL := setupWorld(t)

	src, err := pgx.Connect(ctx, srcURL)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close(ctx)
	for _, q := range []string{
		`CREATE TYPE tier AS ENUM ('basic','premium')`,
		`CREATE TABLE customers (id text PRIMARY KEY, tier tier NOT NULL, balance int)`,
		`INSERT INTO customers SELECT 'c'||i, (ARRAY['basic','premium']::tier[])[1+i%2], i*10
		   FROM generate_series(1, 1500) i`,
	} {
		if _, err := src.Exec(ctx, q); err != nil {
			t.Fatalf("seed source: %v", err)
		}
	}

	// Tenancy: org + project (seeds an owner role + database on main).
	kr := keyring(t)
	res, err := st.Bootstrap(ctx, store.BootstrapParams{
		Email: "o@example.com", OrgName: "Acme",
		KeyName: "owner", KeyHash: "h", KeyPrefix: "p", Scopes: domain.AllScopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	seedCT, seedVer, _ := kr.Encrypt([]byte("seed-pw"))
	pr, err := st.CreateProject(ctx, store.CreateProjectParams{
		OrgID: res.Org.ID, Name: "Shop", Region: "syd1", PGVersion: 17,
		Seed: &store.ProjectSeed{RoleName: "shop_owner", DatabaseName: "shop",
			Secret: store.SecretMaterial{Ciphertext: seedCT, KeyVersion: seedVer}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Register the import with its source URL as an encrypted secret.
	srcCT, srcVer, err := kr.Encrypt([]byte(srcURL))
	if err != nil {
		t.Fatal(err)
	}
	im, err := st.CreateImport(ctx, store.CreateImportParams{
		OrgID: res.Org.ID, ProjectID: pr.ID, SourceKind: domain.ImportGeneric,
		Mode:         domain.ImportDumpRestore,
		SourceSecret: store.SecretMaterial{Ciphertext: srcCT, KeyVersion: srcVer},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Worker: local resolver points the target at our scratch database.
	adapter := importworker.NewAdapter(st, kr, func(context.Context, *store.ClaimedImport) (string, error) {
		return tgtURL, nil
	})
	r := runner.New(adapter, runner.Options{BinDir: binDir()})

	// Drive to completion, simulating the operator's cutover gate.
	for i := 0; i < 40; i++ {
		job, err := r.Step(ctx)
		if err != nil {
			t.Fatalf("step: %v", err)
		}
		if job != nil {
			continue
		}
		cur, err := st.GetImport(ctx, res.Org.ID, im.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch cur.State {
		case domain.ImportVerified:
			// Data actually landed via the worker-driven migration.
			var n int
			tconn, _ := pgx.Connect(ctx, tgtURL)
			defer tconn.Close(ctx)
			if err := tconn.QueryRow(ctx, `SELECT count(*) FROM customers`).Scan(&n); err != nil || n != 1500 {
				t.Fatalf("target rows = %d (%v), want 1500", n, err)
			}
			return
		case domain.ImportCutoverReady:
			// Operator gate (not the worker's to cross).
			if _, err := st.TransitionImportByID(ctx, im.ID,
				store.TransitionImportParams{To: domain.ImportCutOver}); err != nil {
				t.Fatal(err)
			}
		case domain.ImportFailed, domain.ImportAborted:
			t.Fatalf("import ended %s: %v", cur.State, cur.Error)
		default:
			t.Fatalf("queue empty but import stuck in %s", cur.State)
		}
	}
	t.Fatal("migration did not complete within the step budget")
}

func TestWorkerFailsUndecryptableImport(t *testing.T) {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	st, _, _ := setupWorld(t)

	res, err := st.Bootstrap(ctx, store.BootstrapParams{
		Email: "o@example.com", OrgName: "Acme", KeyName: "k", KeyHash: "h", KeyPrefix: "p",
		Scopes: domain.AllScopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := st.CreateProject(ctx, store.CreateProjectParams{OrgID: res.Org.ID, Name: "P", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}
	// Ciphertext the worker's keyring cannot open.
	im, err := st.CreateImport(ctx, store.CreateImportParams{
		OrgID: res.Org.ID, ProjectID: pr.ID, SourceKind: domain.ImportGeneric, Mode: domain.ImportDumpRestore,
		SourceSecret: store.SecretMaterial{Ciphertext: []byte("not-a-valid-blob"), KeyVersion: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	adapter := importworker.NewAdapter(st, keyring(t), func(context.Context, *store.ClaimedImport) (string, error) {
		return "", nil
	})
	if _, err := runner.New(adapter, runner.Options{}).Step(ctx); err == nil {
		t.Fatal("expected a decrypt error")
	}
	cur, _ := st.GetImport(ctx, res.Org.ID, im.ID)
	if cur.State != domain.ImportFailed {
		t.Fatalf("undecryptable import must be marked failed, got %s", cur.State)
	}
}

func withDB(rawURL, db string) string {
	u, _ := url.Parse(rawURL)
	u.Path = "/" + db
	return u.String()
}

// setupWorld creates three dedicated databases (a migrated control-plane DB
// plus source and target app DBs) so the worker test never shares state with
// the store suite's schema-recreating harness. Returns the store and the
// source/target URLs; all three databases are dropped on cleanup.
func setupWorld(t *testing.T) (st *postgres.Store, srcURL, tgtURL string) {
	t.Helper()
	adminURL := os.Getenv("TEST_DATABASE_URL")
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano() % 1_000_000_000
	cpDB := fmt.Sprintf("ndb_iw_cp_%d", stamp)
	srcDB := fmt.Sprintf("ndb_iw_src_%d", stamp)
	tgtDB := fmt.Sprintf("ndb_iw_tgt_%d", stamp)
	for _, db := range []string{cpDB, srcDB, tgtDB} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, db := range []string{cpDB, srcDB, tgtDB} {
			_, _ = admin.Exec(cctx, "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		}
		_ = admin.Close(cctx)
	})

	st, err = postgres.New(ctx, withDB(adminURL, cpDB))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := postgres.Migrate(ctx, st.Pool()); err != nil {
		t.Fatal(err)
	}
	return st, withDB(adminURL, srcDB), withDB(adminURL, tgtDB)
}
