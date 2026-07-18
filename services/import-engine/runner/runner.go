// Package runner orchestrates the import engine against the control-plane
// import state machine (MIGRATION_STRATEGY §2). It is deliberately transport-
// agnostic: the ControlPlane interface is implemented by an HTTP client
// against the control-plane API in production and by a fake in tests. The
// runner owns *how* each stage executes; the control plane owns *what state
// is legal next* (it rejects illegal transitions, so the runner cannot
// corrupt a job even with a stale view).
package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zenulbashar/DB/services/import-engine/internal/dumprestore"
	"github.com/zenulbashar/DB/services/import-engine/internal/logicalrepl"
	"github.com/zenulbashar/DB/services/import-engine/internal/preflight"
	"github.com/zenulbashar/DB/services/import-engine/internal/verify"
)

// Job is the control-plane's view of an import, as the runner needs it.
type Job struct {
	ID             string
	Mode           string // dump_restore | logical_replication
	SourceKind     string
	State          string
	SourceURL      string // decrypted by the control plane for the runner
	TargetURL      string // target branch's direct endpoint (session mode)
	TargetConnInfo string // libpq conninfo the TARGET server uses to reach the source
}

// ControlPlane is the runner's view of the control plane.
type ControlPlane interface {
	// Claim returns the next actionable import, or nil if the queue is empty.
	Claim(ctx context.Context) (*Job, error)
	// Transition advances the job; report/checkpoints/errMsg are optional.
	// The control plane rejects illegal transitions.
	Transition(ctx context.Context, id, to string, report, checkpoints map[string]any, errMsg *string) error
}

type Options struct {
	BinDir   string        // pg_dump/pg_restore location
	SyncPoll time.Duration // logical-replication lag poll
	SyncName string        // publication/subscription/slot base; per-job suffix added
}

type Runner struct {
	cp   ControlPlane
	opts Options
}

func New(cp ControlPlane, opts Options) *Runner {
	if opts.SyncPoll == 0 {
		opts.SyncPoll = 2 * time.Second
	}
	return &Runner{cp: cp, opts: opts}
}

// Step claims one job and advances it at most one state. It returns the job
// ONLY when it actually made progress (a state transition) or hit an error;
// it returns (nil, nil) both when the queue is empty AND when a claimed job is
// legitimately waiting (initial copy not done, replication lag not yet zero).
// That distinction lets the drain loop idle on its ticker instead of
// hot-spinning on a not-yet-ready job (audit finding). Any stage error
// transitions the job to failed — and for logical-replication jobs also tears
// down the replication link so a failed migration cannot leak a WAL-retaining
// slot (audit finding).
func (r *Runner) Step(ctx context.Context) (*Job, error) {
	job, err := r.cp.Claim(ctx)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, nil
	}
	progressed, err := r.advance(ctx, job)
	if err != nil {
		r.cleanupOnFailure(ctx, job)
		msg := err.Error()
		// Best-effort fail marking; the control plane rejects fail-from-
		// terminal, which is fine (nothing to do).
		_ = r.cp.Transition(ctx, job.ID, "failed", nil, nil, &msg)
		return job, err
	}
	if !progressed {
		return nil, nil
	}
	return job, nil
}

// advance drives one stage. It returns progressed=true only when it issued a
// state transition; false means the job stays in its current state (a legal
// wait), which must NOT be turned into a self-transition (the state machine
// has no self-edges — issuing one fails the whole migration; audit finding).
func (r *Runner) advance(ctx context.Context, job *Job) (bool, error) {
	switch job.State {
	case "pending":
		return true, r.cp.Transition(ctx, job.ID, "preflight", nil, nil, nil)

	case "preflight":
		report, err := r.runPreflight(ctx, job)
		if err != nil {
			return false, err
		}
		return true, r.cp.Transition(ctx, job.ID, "schema_copy", report, nil, nil)

	case "schema_copy":
		return true, r.runSchemaCopy(ctx, job)

	case "initial_copy":
		return r.waitInitialCopy(ctx, job)

	case "live_sync":
		return r.runCutoverReady(ctx, job)

	case "cut_over":
		return true, r.runVerify(ctx, job)

	default:
		// pending-cutover (human gate) and terminal states are not the
		// runner's to advance.
		return false, nil
	}
}

// cleanupOnFailure tears down replication for a failing logical job so a
// half-set-up sync cannot leak a slot/publication. It dials the two ends
// independently: if the target is unreachable (a common failure mode) it can
// still drop the WAL-retaining slot on the source, which is the part that
// matters. Best-effort and safe to call even if nothing was set up (the drop
// helpers use existence checks).
func (r *Runner) cleanupOnFailure(ctx context.Context, job *Job) {
	if job.Mode != "logical_replication" {
		return
	}
	sync := r.syncFor(job)
	source, sErr := pgx.Connect(ctx, job.SourceURL)
	if sErr == nil {
		defer source.Close(ctx)
	}
	target, tErr := pgx.Connect(ctx, job.TargetURL)
	if tErr == nil {
		defer target.Close(ctx)
	}
	switch {
	case sErr == nil && tErr == nil:
		_ = logicalrepl.Abort(ctx, source, target, sync)
	case sErr == nil:
		// Target down: at least free the source's slot + publication.
		_ = logicalrepl.DropSourceObjects(ctx, source, sync)
	}
}

func (r *Runner) runPreflight(ctx context.Context, job *Job) (map[string]any, error) {
	conn, err := pgx.Connect(ctx, job.SourceURL)
	if err != nil {
		return nil, fmt.Errorf("preflight connect: %w", err)
	}
	defer conn.Close(ctx)
	rep, err := preflight.Run(ctx, conn, preflight.SourceKind(job.SourceKind))
	if err != nil {
		return nil, err
	}
	if len(rep.Blockers) > 0 {
		return nil, fmt.Errorf("preflight blocked: %s", rep.Blockers[0].Message)
	}
	return map[string]any{
		"server_version":   rep.ServerVersion,
		"database_bytes":   rep.DatabaseSizeBytes,
		"recommended_mode": string(rep.RecommendedMode),
		"tables":           len(rep.Tables),
		"warnings":         len(rep.Warnings),
	}, nil
}

func (r *Runner) runSchemaCopy(ctx context.Context, job *Job) error {
	if job.Mode == "dump_restore" {
		res, err := dumprestore.Run(ctx, dumprestore.Options{
			SourceURL: job.SourceURL, TargetURL: job.TargetURL, BinDir: r.opts.BinDir,
		})
		if err != nil {
			return err
		}
		return r.cp.Transition(ctx, job.ID, "cutover_ready",
			nil, map[string]any{"archive_bytes": res.ArchiveBytes}, nil)
	}

	// logical: schema only now; data flows via the subscription.
	if _, err := dumprestore.Run(ctx, dumprestore.Options{
		SourceURL: job.SourceURL, TargetURL: job.TargetURL, BinDir: r.opts.BinDir, SchemaOnly: true,
	}); err != nil {
		return err
	}
	sync := r.syncFor(job)
	source, target, err := r.dial(ctx, job)
	if err != nil {
		return err
	}
	defer source.Close(ctx)
	defer target.Close(ctx)
	if err := logicalrepl.Setup(ctx, source, target, sync); err != nil {
		return err
	}
	return r.cp.Transition(ctx, job.ID, "initial_copy", nil, nil, nil)
}

func (r *Runner) waitInitialCopy(ctx context.Context, job *Job) (bool, error) {
	source, target, err := r.dial(ctx, job)
	if err != nil {
		return false, err
	}
	defer source.Close(ctx)
	defer target.Close(ctx)
	done, pending, err := logicalrepl.InitialCopyDone(ctx, target, r.syncFor(job))
	if err != nil {
		return false, err
	}
	if !done {
		// Legal wait: stay in initial_copy; the next Step re-checks. Do NOT
		// issue a transition.
		return false, nil
	}
	return true, r.cp.Transition(ctx, job.ID, "live_sync",
		nil, map[string]any{"tables_pending": pending}, nil)
}

func (r *Runner) runCutoverReady(ctx context.Context, job *Job) (bool, error) {
	source, err := r.dialSource(ctx, job)
	if err != nil {
		return false, err
	}
	defer source.Close(ctx)
	lag, err := logicalrepl.LagBytes(ctx, source, r.syncFor(job))
	if err != nil {
		return false, err
	}
	if lag > 0 {
		// Legal wait: replication is still catching up. Stay in live_sync —
		// a live_sync -> live_sync self-transition is illegal and would fail
		// the migration (audit finding); it almost always has non-zero lag on
		// the first check, so this path is the common case.
		return false, nil
	}
	return true, r.cp.Transition(ctx, job.ID, "cutover_ready", nil, map[string]any{"lag_bytes": 0}, nil)
}

func (r *Runner) runVerify(ctx context.Context, job *Job) error {
	source, target, err := r.dial(ctx, job)
	if err != nil {
		return err
	}
	defer source.Close(ctx)
	defer target.Close(ctx)

	// For logical mode, sync sequences (verify checks target >= source) but do
	// NOT tear the replication link down yet. Verify parity FIRST, while the
	// source is still frozen and the link intact, so a failed verify is
	// recoverable and can distinguish incomplete sync from corruption (audit
	// finding). Only cut over once parity holds.
	if job.Mode == "logical_replication" {
		if _, err := logicalrepl.SyncSequences(ctx, source, target, 0); err != nil {
			return err
		}
	}

	v, err := verify.Run(ctx, source, target, verify.Options{})
	if err != nil {
		return err
	}
	if !v.OK() {
		return fmt.Errorf("verification found %d mismatch(es), first: %s/%s",
			len(v.Mismatches), v.Mismatches[0].Kind, v.Mismatches[0].Object)
	}

	if job.Mode == "logical_replication" {
		if err := logicalrepl.Cutover(ctx, source, target, r.syncFor(job)); err != nil {
			return err
		}
	}

	return r.cp.Transition(ctx, job.ID, "verified", nil, map[string]any{
		"tables_checked": v.TablesChecked, "rows": v.RowsTarget, "checksum_ok": true,
	}, nil)
}

func (r *Runner) syncFor(job *Job) logicalrepl.Sync {
	base := r.opts.SyncName
	if base == "" {
		base = "ndb_import"
	}
	// Job IDs are ULIDs with a prefix; sanitize to an identifier-safe suffix.
	suffix := ""
	for _, c := range job.ID {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			suffix += string(c)
		}
	}
	return logicalrepl.Sync{Name: base + "_" + suffix, SourceConnInfo: job.TargetConnInfo}
}

func (r *Runner) dial(ctx context.Context, job *Job) (source, target *pgx.Conn, err error) {
	source, err = pgx.Connect(ctx, job.SourceURL)
	if err != nil {
		return nil, nil, fmt.Errorf("dial source: %w", err)
	}
	target, err = pgx.Connect(ctx, job.TargetURL)
	if err != nil {
		source.Close(ctx)
		return nil, nil, fmt.Errorf("dial target: %w", err)
	}
	return source, target, nil
}

// dialSource opens only the source connection (the lag check needs nothing
// else — the previous code dialed the target too and leaked it every poll).
func (r *Runner) dialSource(ctx context.Context, job *Job) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, job.SourceURL)
	if err != nil {
		return nil, fmt.Errorf("dial source: %w", err)
	}
	return conn, nil
}
