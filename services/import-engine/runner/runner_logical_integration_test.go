//go:build integration

// Drives a full LOGICAL_REPLICATION migration through the runner's Step loop
// against real databases (wal_level=logical). This exercises the paths the
// dump_restore test can't: waitInitialCopy, runCutoverReady (the lag>0 wait
// that must NOT self-transition), verify-then-cutover, and slot teardown. It
// regresses the audit's critical live_sync self-transition bug and the slot
// leak.
package runner

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// logicalCP enforces the real logical-mode transition rules — crucially it
// REJECTS a live_sync -> live_sync self-transition, so the old runner code
// would fail this test.
type logicalCP struct {
	job         *Job
	transitions []string
}

var logicalEdges = map[string]map[string]bool{
	"pending":       {"preflight": true, "failed": true},
	"preflight":     {"schema_copy": true, "failed": true},
	"schema_copy":   {"initial_copy": true, "failed": true},
	"initial_copy":  {"live_sync": true, "failed": true},
	"live_sync":     {"cutover_ready": true, "failed": true}, // NOT live_sync
	"cutover_ready": {"cut_over": true, "failed": true},
	"cut_over":      {"verified": true, "failed": true},
}

func (c *logicalCP) Claim(context.Context) (*Job, error) {
	if c.job.State == "cutover_ready" {
		c.job.State = "cut_over" // operator gate
		c.transitions = append(c.transitions, "cut_over(op)")
	}
	if c.job.State == "verified" || c.job.State == "failed" {
		return nil, nil
	}
	return c.job, nil
}

func (c *logicalCP) Transition(_ context.Context, _, to string, _, _ map[string]any, _ *string) error {
	if !logicalEdges[c.job.State][to] {
		return fmt.Errorf("illegal transition %s -> %s", c.job.State, to)
	}
	c.job.State = to
	c.transitions = append(c.transitions, to)
	return nil
}

func TestRunnerDrivesLogicalReplicationToVerified(t *testing.T) {
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
	_ = admin.QueryRow(ctx, `SHOW wal_level`).Scan(&walLevel)
	if walLevel != "logical" {
		t.Skipf("wal_level=%s; need logical", walLevel)
	}

	stamp := time.Now().UnixNano() % 1_000_000
	srcDB := fmt.Sprintf("ndb_rl_src_%d", stamp)
	tgtDB := fmt.Sprintf("ndb_rl_tgt_%d", stamp)
	for _, db := range []string{srcDB, tgtDB} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, db := range []string{tgtDB, srcDB} {
			_, _ = admin.Exec(cctx, "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		}
		_ = admin.Close(cctx)
	})
	mk := func(db string) string {
		u, _ := url.Parse(adminURL)
		u.Path = "/" + db
		return u.String()
	}
	srcURL, tgtURL := mk(srcDB), mk(tgtDB)

	src, err := pgx.Connect(ctx, srcURL)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close(ctx)
	for _, q := range []string{
		`CREATE TABLE events (id serial PRIMARY KEY, kind text NOT NULL, payload text)`,
		`INSERT INTO events (kind, payload) SELECT 'e'||i, repeat('x', 50) FROM generate_series(1, 800) i`,
	} {
		if _, err := src.Exec(ctx, q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	binDir := ""
	if _, err := exec.LookPath("pg_dump"); err != nil {
		for _, d := range []string{"/usr/lib/postgresql/17/bin", "/usr/lib/postgresql/16/bin"} {
			if _, err := os.Stat(d + "/pg_dump"); err == nil {
				binDir = d
			}
		}
	}

	syncName := fmt.Sprintf("ndb_rl_%d", stamp)
	cp := &logicalCP{job: &Job{
		ID: "imp01logical", Mode: "logical_replication", SourceKind: "generic", State: "pending",
		SourceURL: srcURL, TargetURL: tgtURL, TargetConnInfo: connInfo(t, srcURL),
	}}
	r := New(cp, Options{BinDir: binDir, SyncName: syncName})

	deadline := time.Now().Add(60 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("did not converge; transitions=%v state=%s", cp.transitions, cp.job.State)
		}
		if _, err := r.Step(ctx); err != nil {
			t.Fatalf("step (transitions=%v): %v", cp.transitions, err)
		}
		if cp.job.State == "verified" {
			break
		}
		if cp.job.State == "failed" {
			t.Fatalf("migration failed; transitions=%v", cp.transitions)
		}
		time.Sleep(150 * time.Millisecond)
	}

	// Data replicated + copied.
	tgt, err := pgx.Connect(ctx, tgtURL)
	if err != nil {
		t.Fatal(err)
	}
	defer tgt.Close(ctx)
	var n int
	if err := tgt.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&n); err != nil || n != 800 {
		t.Fatalf("target rows = %d (%v), want 800", n, err)
	}
	// The replication slot must be gone after cutover (no WAL leak).
	var slots int
	if err := src.QueryRow(ctx,
		`SELECT count(*) FROM pg_replication_slots WHERE slot_name LIKE $1 || '%'`, syncName).Scan(&slots); err != nil {
		t.Fatal(err)
	}
	if slots != 0 {
		t.Fatalf("replication slot leaked after cutover: %d", slots)
	}
}

func TestRunnerCleansUpSlotOnLogicalFailure(t *testing.T) {
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
	_ = admin.QueryRow(ctx, `SHOW wal_level`).Scan(&walLevel)
	if walLevel != "logical" {
		t.Skipf("wal_level=%s; need logical", walLevel)
	}
	stamp := time.Now().UnixNano() % 1_000_000
	srcDB := fmt.Sprintf("ndb_rlf_src_%d", stamp)
	tgtDB := fmt.Sprintf("ndb_rlf_tgt_%d", stamp)
	for _, db := range []string{srcDB, tgtDB} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, db := range []string{tgtDB, srcDB} {
			_, _ = admin.Exec(cctx, "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		}
		_ = admin.Close(cctx)
	})
	mk := func(db string) string {
		u, _ := url.Parse(adminURL)
		u.Path = "/" + db
		return u.String()
	}
	srcURL, tgtURL := mk(srcDB), mk(tgtDB)
	src, _ := pgx.Connect(ctx, srcURL)
	defer src.Close(ctx)
	_, _ = src.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY)`)

	// Set up replication, then force a failure at the verify stage by pointing
	// the job into cut_over with a broken target so runVerify errors — the
	// runner's failure cleanup must drop the slot.
	binDir := ""
	for _, d := range []string{"/usr/lib/postgresql/17/bin", "/usr/lib/postgresql/16/bin"} {
		if _, err := os.Stat(d + "/pg_dump"); err == nil {
			binDir = d
		}
	}
	// Seed a row on the source so there is data to replicate/compare.
	_, _ = src.Exec(ctx, `INSERT INTO t SELECT generate_series(1, 100)`)

	syncName := fmt.Sprintf("ndb_rlf_%d", stamp)
	cp := &logicalCP{job: &Job{
		ID: "imp01fail", Mode: "logical_replication", SourceKind: "generic", State: "schema_copy",
		SourceURL: srcURL, TargetURL: tgtURL, TargetConnInfo: connInfo(t, srcURL),
	}}
	r := New(cp, Options{BinDir: binDir, SyncName: syncName})

	// Drive normally until the slot exists and we're ready for the operator gate.
	deadline := time.Now().Add(30 * time.Second)
	for cp.job.State != "cutover_ready" {
		if time.Now().After(deadline) {
			t.Fatalf("did not reach cutover_ready; state=%s transitions=%v", cp.job.State, cp.transitions)
		}
		if _, err := r.Step(ctx); err != nil {
			t.Fatalf("drive: %v", err)
		}
		time.Sleep(120 * time.Millisecond)
	}
	var slots int
	_ = src.QueryRow(ctx, `SELECT count(*) FROM pg_replication_slots WHERE slot_name LIKE $1 || '%'`, syncName).Scan(&slots)
	if slots != 1 {
		t.Fatalf("expected 1 slot at cutover_ready, got %d", slots)
	}

	// Diverge the (reachable) target so the cut_over verify fails. The failure
	// cleanup must Abort — disabling the subscription and dropping the slot.
	tgt, err := pgx.Connect(ctx, tgtURL)
	if err != nil {
		t.Fatal(err)
	}
	defer tgt.Close(ctx)
	if _, err := tgt.Exec(ctx, `INSERT INTO t VALUES (999999)`); err != nil {
		t.Fatal(err)
	}

	// Operator gate (logicalCP.Claim flips cutover_ready -> cut_over), then the
	// verify runs and fails on the row-count mismatch.
	if _, err := r.Step(ctx); err == nil {
		t.Fatalf("expected verify failure; transitions=%v", cp.transitions)
	}
	if cp.job.State != "failed" {
		t.Fatalf("job state = %s, want failed", cp.job.State)
	}
	_ = src.QueryRow(ctx, `SELECT count(*) FROM pg_replication_slots WHERE slot_name LIKE $1 || '%'`, syncName).Scan(&slots)
	if slots != 0 {
		t.Fatalf("slot leaked after logical-job failure: %d", slots)
	}
}

func connInfo(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	info := fmt.Sprintf("host=%s port=%s dbname=%s user=%s", u.Hostname(), port, u.Path[1:], u.User.Username())
	if pw, ok := u.User.Password(); ok {
		info += " password=" + pw
	}
	return info
}
