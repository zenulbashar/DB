//go:build integration

// Regression for the audit finding that a bounded sampled checksum could miss
// a single-row content diff in any table larger than the sample. The default
// (SampleRows 0) must checksum the FULL table and catch it.
package verify

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func twoDBs(t *testing.T) (src, tgt *pgx.Conn) {
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
	sName := fmt.Sprintf("ndb_vrfy_s_%d", stamp)
	tName := fmt.Sprintf("ndb_vrfy_t_%d", stamp)
	for _, db := range []string{sName, tName} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, db := range []string{sName, tName} {
			_, _ = admin.Exec(cctx, "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		}
		_ = admin.Close(cctx)
	})
	mk := func(db string) string {
		u, _ := url.Parse(adminURL)
		u.Path = "/" + db
		return u.String()
	}
	src, err = pgx.Connect(ctx, mk(sName))
	if err != nil {
		t.Fatal(err)
	}
	tgt, err = pgx.Connect(ctx, mk(tName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close(context.Background()); _ = tgt.Close(context.Background()) })

	// 5000 rows — well beyond any bounded sample default.
	for _, c := range []*pgx.Conn{src, tgt} {
		if _, err := c.Exec(ctx,
			`CREATE TABLE ledger (id int PRIMARY KEY, amount bigint NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(ctx,
			`INSERT INTO ledger SELECT i, i*100 FROM generate_series(1, 5000) i`); err != nil {
			t.Fatal(err)
		}
	}
	return src, tgt
}

func TestFullTableChecksumCatchesOutOfSampleCorruption(t *testing.T) {
	src, tgt := twoDBs(t)
	ctx := context.Background()

	// Identical tables verify clean by default.
	v, err := Run(ctx, src, tgt, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !v.OK() {
		t.Fatalf("identical tables should verify clean: %+v", v.Mismatches)
	}

	// Corrupt ONE row far outside any 1000-row sample window — same row count,
	// different content. The default full-table checksum must catch it.
	if _, err := tgt.Exec(ctx, `UPDATE ledger SET amount = amount + 1 WHERE id = 4321`); err != nil {
		t.Fatal(err)
	}
	v, err = Run(ctx, src, tgt, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if v.OK() {
		t.Fatal("full-table verify MISSED a single out-of-sample row corruption")
	}
	found := false
	for _, m := range v.Mismatches {
		if m.Kind == "checksum" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a checksum mismatch, got %+v", v.Mismatches)
	}
}
