//go:build integration

// Full logical-replication migration rehearsal against a real Postgres with
// wal_level=logical: schema copy → subscribe (initial copy) → live writes
// replicate → freeze → lag zero → sequence sync → cutover → verify parity.
// This is the miniature of the Roster/Prompt2Eat production cutovers
// (MIGRATION_STRATEGY §5–7).
package logicalrepl

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zenulbashar/DB/services/import-engine/internal/dumprestore"
	"github.com/zenulbashar/DB/services/import-engine/internal/verify"
)

type replEnv struct {
	source, target       *pgx.Conn
	sourceURL, targetURL string
	binDir               string
}

func setup(t *testing.T) *replEnv {
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

	var walLevel string
	if err := admin.QueryRow(ctx, `SHOW wal_level`).Scan(&walLevel); err != nil {
		t.Fatal(err)
	}
	if walLevel != "logical" {
		t.Skipf("wal_level=%s; logical replication tests need wal_level=logical", walLevel)
	}

	stamp := time.Now().UnixNano() % 1_000_000
	srcName := fmt.Sprintf("ndb_lr_src_%d", stamp)
	tgtName := fmt.Sprintf("ndb_lr_tgt_%d", stamp)
	for _, db := range []string{srcName, tgtName} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, db := range []string{tgtName, srcName} { // target first: drops the subscription's slot user
			_, _ = admin.Exec(cctx, "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		}
		_ = admin.Close(cctx)
	})

	mkURL := func(db string) string {
		u, _ := url.Parse(adminURL)
		u.Path = "/" + db
		return u.String()
	}
	env := &replEnv{sourceURL: mkURL(srcName), targetURL: mkURL(tgtName)}

	if _, err := exec.LookPath("pg_dump"); err != nil {
		for _, dir := range []string{"/usr/lib/postgresql/17/bin", "/usr/lib/postgresql/16/bin"} {
			if _, err := os.Stat(dir + "/pg_dump"); err == nil {
				env.binDir = dir
				break
			}
		}
	}

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

	fixtures := []string{
		`CREATE TABLE sessions (id text PRIMARY KEY, user_email text NOT NULL, expires timestamptz)`,
		`CREATE TABLE orders (id serial PRIMARY KEY, item text NOT NULL, qty int NOT NULL)`,
		`INSERT INTO sessions SELECT 's'||i, 'user'||i||'@example.com', now() + interval '30 days'
		   FROM generate_series(1, 300) i`,
		`INSERT INTO orders (item, qty) SELECT 'item-'||i, i FROM generate_series(1, 500) i`,
	}
	for _, q := range fixtures {
		if _, err := env.source.Exec(ctx, q); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	return env
}

// connInfoFor converts a URL into the conninfo the target server dials.
func connInfoFor(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	info := fmt.Sprintf("host=%s port=%s dbname=%s user=%s", host, port, u.Path[1:], u.User.Username())
	if pw, ok := u.User.Password(); ok {
		info += " password=" + pw
	}
	return info
}

func TestLogicalReplicationEndToEnd(t *testing.T) {
	env := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Stage 3: schema-only copy.
	if _, err := dumprestore.Run(ctx, dumprestore.Options{
		SourceURL: env.sourceURL, TargetURL: env.targetURL,
		BinDir: env.binDir, SchemaOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	var targetRows int
	if err := env.target.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&targetRows); err != nil || targetRows != 0 {
		t.Fatalf("schema-only copy leaked data: rows=%d err=%v", targetRows, err)
	}

	// Stage 4: publication + subscription with initial copy.
	s := Sync{Name: "ndb_import_test", SourceConnInfo: connInfoFor(t, env.sourceURL)}
	if err := Setup(ctx, env.source, env.target, s); err != nil {
		t.Fatal(err)
	}
	if err := WaitSynced(ctx, env.source, env.target, s, 500*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	// Live sync: writes on the source flow to the target.
	if _, err := env.source.Exec(ctx,
		`INSERT INTO orders (item, qty) VALUES ('late-arrival', 999)`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.source.Exec(ctx,
		`UPDATE sessions SET expires = now() WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	if err := WaitSynced(ctx, env.source, env.target, s, 500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// Replication apply is async past lag-zero; give the worker a beat, then
	// confirm the late row landed.
	deadline := time.Now().Add(15 * time.Second)
	for {
		var qty int
		err := env.target.QueryRow(ctx, `SELECT qty FROM orders WHERE item = 'late-arrival'`).Scan(&qty)
		if err == nil && qty == 999 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live write did not replicate: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}

	// Stage 5: freeze (no more writes in this test) → sequences → cutover.
	n, err := SyncSequences(ctx, env.source, env.target, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no sequences synced (orders.id serial expected)")
	}
	if err := Cutover(ctx, env.source, env.target, s); err != nil {
		t.Fatal(err)
	}

	// Slot must be gone on the source (nothing left to grow WAL).
	var slots int
	if err := env.source.QueryRow(ctx,
		`SELECT count(*) FROM pg_replication_slots WHERE slot_name = $1`, s.Name).Scan(&slots); err != nil {
		t.Fatal(err)
	}
	if slots != 0 {
		t.Fatal("replication slot survived cutover")
	}

	// Verification: full parity including the sequence position.
	v, err := verify.Run(ctx, env.source, env.target, verify.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !v.OK() {
		t.Fatalf("post-cutover verification failed: %+v", v.Mismatches)
	}

	// The target must be independently writable with no duplicate-key risk.
	if _, err := env.target.Exec(ctx,
		`INSERT INTO orders (item, qty) VALUES ('post-cutover', 1)`); err != nil {
		t.Fatalf("post-cutover insert (sequence collision?): %v", err)
	}
}

func TestSetupIsRetryableAfterSubscriptionFailure(t *testing.T) {
	env := setup(t)
	ctx := context.Background()

	// Break subscription creation with a closed local port (immediate
	// connection refused — keeps the test fast).
	bad := Sync{Name: "ndb_import_bad", SourceConnInfo: "host=127.0.0.1 port=1 dbname=nope user=nobody connect_timeout=1"}
	if err := Setup(ctx, env.source, env.target, bad); err == nil {
		t.Fatal("expected setup failure")
	}
	// The publication must have been rolled back so a corrected retry works.
	var pubs int
	if err := env.source.QueryRow(ctx,
		`SELECT count(*) FROM pg_publication WHERE pubname = 'ndb_import_bad'`).Scan(&pubs); err != nil {
		t.Fatal(err)
	}
	if pubs != 0 {
		t.Fatal("failed setup leaked a publication (not retryable)")
	}
}

func TestSyncNameValidation(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{"", "Bad-Name", "x; DROP TABLE users", "1abc"} {
		if err := Setup(ctx, nil, nil, Sync{Name: bad, SourceConnInfo: "host=x"}); err == nil {
			t.Errorf("name %q must be rejected before any SQL runs", bad)
		}
	}
}
