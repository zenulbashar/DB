//go:build integration

// End-to-end migration test: fixture source database → pg_dump/pg_restore →
// parity verification (the same pipeline the import workflow drives).
package dumprestore

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zenulbashar/DB/services/import-engine/internal/verify"
)

// resolveBinDir finds Postgres client binaries: PATH first, then the
// conventional Debian location matching the local server major.
func resolveBinDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("pg_dump"); err == nil {
		return ""
	}
	for _, dir := range []string{"/usr/lib/postgresql/17/bin", "/usr/lib/postgresql/16/bin"} {
		if _, err := os.Stat(dir + "/pg_dump"); err == nil {
			return dir
		}
	}
	t.Skip("pg_dump not available")
	return ""
}

type migrationEnv struct {
	sourceURL, targetURL string
	source, target       *pgx.Conn
	binDir               string
}

func setup(t *testing.T) *migrationEnv {
	t.Helper()
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}

	stamp := time.Now().UnixNano() % 1_000_000
	srcName := fmt.Sprintf("ndb_mig_src_%d", stamp)
	tgtName := fmt.Sprintf("ndb_mig_tgt_%d", stamp)
	for _, db := range []string{srcName, tgtName} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, db := range []string{srcName, tgtName} {
			_, _ = admin.Exec(cctx, "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		}
		_ = admin.Close(cctx)
	})

	mkURL := func(db string) string {
		u, _ := url.Parse(adminURL)
		u.Path = "/" + db
		return u.String()
	}
	env := &migrationEnv{sourceURL: mkURL(srcName), targetURL: mkURL(tgtName), binDir: resolveBinDir(t)}

	env.source, err = pgx.Connect(ctx, env.sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	env.target, err = pgx.Connect(ctx, env.targetURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = env.source.Close(context.Background())
		_ = env.target.Close(context.Background())
	})

	// Fixture: the shapes the first customers actually have — enums,
	// sequences, FK pairs, and enough rows to make checksums meaningful.
	fixtures := []string{
		`CREATE TYPE order_status AS ENUM ('pending','confirmed','shipped')`,
		`CREATE TABLE venues (id text PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE orders (
		    id text PRIMARY KEY,
		    venue_id text NOT NULL REFERENCES venues(id),
		    status order_status NOT NULL DEFAULT 'pending',
		    total_cents int NOT NULL,
		    placed_at timestamptz NOT NULL DEFAULT now())`,
		`CREATE SEQUENCE order_counter START 5000`,
		`INSERT INTO venues SELECT 'v'||i, 'Venue '||i FROM generate_series(1, 20) i`,
		`INSERT INTO orders
		   SELECT 'o'||i, 'v'||(1 + i % 20),
		          ((ARRAY['pending','confirmed','shipped'])[1 + i % 3])::order_status,
		          i * 100, now() - (i || ' minutes')::interval
		     FROM generate_series(1, 2000) i`,
		`SELECT nextval('order_counter')`,
	}
	for _, q := range fixtures {
		if _, err := env.source.Exec(ctx, q); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	return env
}

func TestDumpRestoreRoundTripVerifies(t *testing.T) {
	env := setup(t)
	ctx := context.Background()

	res, err := Run(ctx, Options{
		SourceURL: env.sourceURL, TargetURL: env.targetURL, BinDir: env.binDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ArchiveBytes == 0 {
		t.Fatal("archive is empty")
	}

	v, err := verify.Run(ctx, env.source, env.target, verify.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !v.OK() {
		t.Fatalf("verification failed after clean migration: %+v", v.Mismatches)
	}
	if v.TablesChecked != 2 || v.RowsSource != 2020 || v.RowsTarget != 2020 {
		t.Fatalf("unexpected verify stats: %+v", v)
	}
}

func TestVerifyCatchesTampering(t *testing.T) {
	env := setup(t)
	ctx := context.Background()

	if _, err := Run(ctx, Options{SourceURL: env.sourceURL, TargetURL: env.targetURL, BinDir: env.binDir}); err != nil {
		t.Fatal(err)
	}

	// Same row count, different content on venues (checksum must catch it);
	// a row-count change on orders (FK-safe direction).
	if _, err := env.target.Exec(ctx, `UPDATE venues SET name = 'Tampered' WHERE id = 'v7'`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.target.Exec(ctx, `DELETE FROM orders WHERE id = 'o42'`); err != nil {
		t.Fatal(err)
	}
	// And a sequence regression.
	if _, err := env.target.Exec(ctx, `SELECT setval('order_counter', 10)`); err != nil {
		t.Fatal(err)
	}

	v, err := verify.Run(ctx, env.source, env.target, verify.Options{SampleRows: 5000})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, m := range v.Mismatches {
		kinds[m.Kind] = true
	}
	for _, want := range []string{"checksum", "row_count", "sequence"} {
		if !kinds[want] {
			t.Errorf("verification missed a %s mismatch: %+v", want, v.Mismatches)
		}
	}
}
