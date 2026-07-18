//go:build integration

// The runner integration test drives a full dump_restore import to `verified`
// through the same state machine the control plane enforces — a fake control
// plane (with the real transition rules) plus two real local databases. This
// is the closest local proxy to the production Roster cutover.
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

// fakeControlPlane replays a single job and enforces the same legal-transition
// rules as the control-plane store (dump_restore path).
type fakeControlPlane struct {
	job         *Job
	transitions []string
	failWith    string
}

var dumpTransitions = map[string]map[string]bool{
	"pending":       {"preflight": true, "failed": true},
	"preflight":     {"schema_copy": true, "failed": true},
	"schema_copy":   {"cutover_ready": true, "failed": true},
	"cutover_ready": {"cut_over": true, "failed": true},
	"cut_over":      {"verified": true, "failed": true},
}

func (f *fakeControlPlane) Claim(context.Context) (*Job, error) {
	// Human gate: the runner never advances a cutover_ready job; the "operator"
	// flips it to cut_over out of band before the runner sees it again.
	if f.job.State == "cutover_ready" {
		f.job.State = "cut_over"
		f.transitions = append(f.transitions, "cut_over(operator)")
	}
	if f.job.State == "verified" || f.job.State == "failed" {
		return nil, nil
	}
	return f.job, nil
}

func (f *fakeControlPlane) Transition(_ context.Context, _, to string, _, checkpoints map[string]any, errMsg *string) error {
	if !dumpTransitions[f.job.State][to] {
		return fmt.Errorf("illegal transition %s -> %s", f.job.State, to)
	}
	if errMsg != nil {
		f.failWith = *errMsg
	}
	f.job.State = to
	f.transitions = append(f.transitions, to)
	return nil
}

type dbEnv struct {
	sourceURL, targetURL string
	source, target       *pgx.Conn
	binDir               string
}

func setupDBs(t *testing.T) *dbEnv {
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
	src := fmt.Sprintf("ndb_run_src_%d", stamp)
	tgt := fmt.Sprintf("ndb_run_tgt_%d", stamp)
	for _, db := range []string{src, tgt} {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, db := range []string{src, tgt} {
			_, _ = admin.Exec(cctx, "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		}
		_ = admin.Close(cctx)
	})
	mkURL := func(db string) string {
		u, _ := url.Parse(adminURL)
		u.Path = "/" + db
		return u.String()
	}
	env := &dbEnv{sourceURL: mkURL(src), targetURL: mkURL(tgt)}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		for _, dir := range []string{"/usr/lib/postgresql/17/bin", "/usr/lib/postgresql/16/bin"} {
			if _, err := os.Stat(dir + "/pg_dump"); err == nil {
				env.binDir = dir
			}
		}
	}
	env.source, _ = pgx.Connect(ctx, env.sourceURL)
	env.target, _ = pgx.Connect(ctx, env.targetURL)
	t.Cleanup(func() {
		_ = env.source.Close(context.Background())
		_ = env.target.Close(context.Background())
	})
	for _, q := range []string{
		`CREATE TYPE plan AS ENUM ('free','pro')`,
		`CREATE TABLE accounts (id text PRIMARY KEY, plan plan NOT NULL, credits int)`,
		`INSERT INTO accounts SELECT 'a'||i, (ARRAY['free','pro']::plan[])[1 + i % 2], i
		   FROM generate_series(1, 400) i`,
	} {
		if _, err := env.source.Exec(ctx, q); err != nil {
			t.Fatalf("seed source: %v", err)
		}
	}
	return env
}

// drive runs the runner until the job reaches a terminal state (bounded).
func drive(t *testing.T, r *Runner, cp *fakeControlPlane) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		job, err := r.Step(ctx)
		if err != nil {
			t.Fatalf("step: %v", err)
		}
		if job == nil {
			return
		}
	}
	t.Fatal("runner did not terminate within 20 steps")
}

func TestRunnerDrivesDumpRestoreToVerified(t *testing.T) {
	env := setupDBs(t)
	cp := &fakeControlPlane{job: &Job{
		ID: "imp_01test", Mode: "dump_restore", SourceKind: "generic", State: "pending",
		SourceURL: env.sourceURL, TargetURL: env.targetURL,
	}}
	r := New(cp, Options{BinDir: env.binDir})

	drive(t, r, cp)

	if cp.job.State != "verified" {
		t.Fatalf("final state = %s (%q); transitions: %v", cp.job.State, cp.failWith, cp.transitions)
	}
	// The operator flips cutover_ready→cut_over out of band (recorded inside
	// the fake's Claim, not via Transition), so the runner-driven transitions
	// are: preflight, schema_copy, cutover_ready, then (operator), then verified.
	want := []string{"preflight", "schema_copy", "cutover_ready", "cut_over(operator)", "verified"}
	if len(cp.transitions) != len(want) {
		t.Fatalf("transitions = %v, want %v", cp.transitions, want)
	}
	for i, w := range want {
		if cp.transitions[i] != w {
			t.Fatalf("transitions[%d] = %s, want %s (all: %v)", i, cp.transitions[i], w, cp.transitions)
		}
	}

	// Data actually landed on the target.
	var n int
	if err := env.target.QueryRow(context.Background(), `SELECT count(*) FROM accounts`).Scan(&n); err != nil || n != 400 {
		t.Fatalf("target rows = %d (%v), want 400", n, err)
	}
}

func TestRunnerFailsJobOnBadSource(t *testing.T) {
	env := setupDBs(t)
	cp := &fakeControlPlane{job: &Job{
		ID: "imp_01bad", Mode: "dump_restore", SourceKind: "generic", State: "preflight",
		SourceURL: "postgres://nobody@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1",
		TargetURL: env.targetURL,
	}}
	r := New(cp, Options{BinDir: env.binDir})

	job, err := r.Step(context.Background())
	if err == nil {
		t.Fatal("expected step error on unreachable source")
	}
	if job == nil || cp.job.State != "failed" {
		t.Fatalf("job must be marked failed, state=%s", cp.job.State)
	}
	if cp.failWith == "" {
		t.Fatal("failure message not recorded")
	}
}
