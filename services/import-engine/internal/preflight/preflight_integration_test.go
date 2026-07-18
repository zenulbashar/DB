//go:build integration

// Preflight integration tests run against a real Postgres with a fixture
// schema resembling the first customers' shapes (enums, PK-less table,
// sequences): TEST_DATABASE_URL=… go test -tags=integration ./...
package preflight

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func fixtureConn(t *testing.T) *pgx.Conn {
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
	dbName := fmt.Sprintf("ndb_preflight_%d", time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(cctx, "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
		_ = admin.Close(cctx)
	})

	u, _ := url.Parse(adminURL)
	u.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	fixtures := []string{
		`CREATE TYPE order_status AS ENUM ('pending','confirmed','shipped')`,
		`CREATE TABLE orders (id text PRIMARY KEY, status order_status NOT NULL, total int)`,
		`CREATE TABLE audit_trail (event text, at timestamptz)`, // deliberately PK-less
		`CREATE TABLE staff (id serial PRIMARY KEY, email text UNIQUE)`,
		`CREATE SEQUENCE invoice_counter`,
		`INSERT INTO orders SELECT 'o'||i, 'confirmed', i FROM generate_series(1, 500) i`,
		`ANALYZE orders`,
	}
	for _, q := range fixtures {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return conn
}

func TestPreflightReport(t *testing.T) {
	conn := fixtureConn(t)
	r, err := Run(context.Background(), conn, SourceNeon)
	if err != nil {
		t.Fatal(err)
	}

	if r.VersionNum < 100000 {
		t.Fatalf("version_num = %d", r.VersionNum)
	}
	if r.DatabaseSizeBytes <= 0 {
		t.Fatal("database size not measured")
	}
	if len(r.Tables) != 3 {
		t.Fatalf("tables = %d, want 3 (%+v)", len(r.Tables), r.Tables)
	}
	if len(r.TablesWithoutPK) != 1 || r.TablesWithoutPK[0] != "public.audit_trail" {
		t.Fatalf("tables_without_pk = %v", r.TablesWithoutPK)
	}
	if len(r.Enums) != 1 || r.Enums[0] != "order_status" {
		t.Fatalf("enums = %v", r.Enums)
	}
	// staff.id serial creates one sequence + explicit invoice_counter = 2.
	if r.SequenceCount != 2 {
		t.Fatalf("sequences = %d, want 2", r.SequenceCount)
	}
	if r.RecommendedMode != ModeDumpRestore {
		t.Fatalf("mode = %s, want dump_restore for a tiny db", r.RecommendedMode)
	}

	codes := map[string]Finding{}
	for _, f := range r.Warnings {
		codes[f.Code] = f
	}
	if _, ok := codes["tables-without-pk"]; !ok {
		t.Fatalf("missing tables-without-pk warning: %+v", r.Warnings)
	}
	if f, ok := codes["neon-source"]; !ok || !strings.Contains(f.Remediation, "unpooled") {
		t.Fatalf("missing/incomplete neon-source note: %+v", r.Warnings)
	}
	// Stock test Postgres runs wal_level=replica → warning present with
	// source-specific remediation.
	if f, ok := codes["wal-level"]; ok {
		if !strings.Contains(f.Remediation, "logical replication") &&
			!strings.Contains(f.Remediation, "wal_level") {
			t.Fatalf("wal-level remediation unhelpful: %+v", f)
		}
	}
	if len(r.Blockers) != 0 {
		t.Fatalf("unexpected blockers: %+v", r.Blockers)
	}
}

func TestPreflightReplicaIdentityFullClearsWarning(t *testing.T) {
	conn := fixtureConn(t)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `ALTER TABLE audit_trail REPLICA IDENTITY FULL`); err != nil {
		t.Fatal(err)
	}
	r, err := Run(ctx, conn, SourceGeneric)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.TablesWithoutPK) != 0 {
		t.Fatalf("REPLICA IDENTITY FULL must clear the finding: %v", r.TablesWithoutPK)
	}
}

func TestPreflightSupabaseNote(t *testing.T) {
	conn := fixtureConn(t)
	r, err := Run(context.Background(), conn, SourceSupabase)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range r.Warnings {
		if w.Code == "supabase-source" && strings.Contains(w.Message, "auth") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing supabase platform-schema note: %+v", r.Warnings)
	}
}
