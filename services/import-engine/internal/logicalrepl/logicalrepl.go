// Package logicalrepl drives the logical_replication migration mode
// (MIGRATION_STRATEGY §2 stages 4–5): publication on the source,
// subscription with initial copy on the target, lag measurement,
// sequence sync, and cutover/abort teardown.
//
// Requirements enforced by preflight: source wal_level=logical, free
// replication slots, REPLICATION-capable role. Target-side CREATE
// SUBSCRIPTION needs superuser (or pg_create_subscription on PG ≥ 16) —
// on NimbusDB targets the import runs with a platform-issued import role.
package logicalrepl

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

var nameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

type Sync struct {
	// Name is used for both publication and subscription (and the slot),
	// e.g. "ndb_import_01jz…" — lowercase snake, validated.
	Name string
	// SourceConnInfo is the conninfo the TARGET SERVER uses to reach the
	// source (must be reachable from the target's network, carry credentials,
	// and point at the source's direct/session endpoint — poolers cannot hold
	// replication slots).
	SourceConnInfo string
}

func (s Sync) validate() error {
	if !nameRE.MatchString(s.Name) {
		return fmt.Errorf("logicalrepl: invalid sync name %q", s.Name)
	}
	if s.SourceConnInfo == "" {
		return fmt.Errorf("logicalrepl: SourceConnInfo required")
	}
	return nil
}

// Setup creates the publication and replication slot (source), then the
// subscription (target) with initial data copy. Schema must already exist on
// the target (dumprestore.Options{SchemaOnly: true}).
//
// The slot is created EXPLICITLY and the subscription uses create_slot=false:
// automatic slot creation deadlocks when publisher and subscriber share a
// cluster (slot creation waits on the CREATE SUBSCRIPTION transaction
// itself), and the explicit slot also makes Setup cleanly retryable.
func Setup(ctx context.Context, source, target *pgx.Conn, s Sync) error {
	if err := s.validate(); err != nil {
		return err
	}
	if _, err := source.Exec(ctx,
		fmt.Sprintf(`CREATE PUBLICATION %s FOR ALL TABLES`, s.Name)); err != nil {
		return fmt.Errorf("create publication: %w", err)
	}
	rollback := func() {
		_, _ = source.Exec(ctx, `SELECT pg_drop_replication_slot(slot_name)
			FROM pg_replication_slots WHERE slot_name = '`+s.Name+`'`)
		_, _ = source.Exec(ctx, `DROP PUBLICATION IF EXISTS `+s.Name)
	}
	if _, err := source.Exec(ctx,
		`SELECT pg_create_logical_replication_slot($1, 'pgoutput')`, s.Name); err != nil {
		rollback()
		return fmt.Errorf("create replication slot: %w", err)
	}
	// quote_literal via parameter is unavailable in DDL; conninfo is
	// platform-constructed, but escape single quotes defensively anyway.
	conninfo := escapeLiteral(s.SourceConnInfo)
	if _, err := target.Exec(ctx, fmt.Sprintf(
		`CREATE SUBSCRIPTION %s CONNECTION %s PUBLICATION %s
		   WITH (copy_data = true, create_slot = false, slot_name = %s)`,
		s.Name, conninfo, s.Name, s.Name)); err != nil {
		rollback()
		return fmt.Errorf("create subscription: %w", err)
	}
	return nil
}

// InitialCopyDone reports whether every subscribed table finished its copy
// (pg_subscription_rel srsubstate 'r' = ready, 's' = synchronized).
func InitialCopyDone(ctx context.Context, target *pgx.Conn, s Sync) (done bool, pending int, err error) {
	err = target.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE sr.srsubstate NOT IN ('r', 's'))
		  FROM pg_subscription_rel sr
		  JOIN pg_subscription sub ON sub.oid = sr.srsubid
		 WHERE sub.subname = $1`, s.Name).Scan(&pending)
	if err != nil {
		return false, 0, err
	}
	// Zero rows in pg_subscription_rel means the copy workers haven't
	// registered yet; treat as not done.
	var total int
	if err := target.QueryRow(ctx, `
		SELECT count(*) FROM pg_subscription_rel sr
		  JOIN pg_subscription sub ON sub.oid = sr.srsubid
		 WHERE sub.subname = $1`, s.Name).Scan(&total); err != nil {
		return false, 0, err
	}
	return total > 0 && pending == 0, pending, nil
}

// LagBytes measures replication lag from the SOURCE side: WAL written vs the
// slot's confirmed flush position (surfaced as imports.lag_bytes in the API).
func LagBytes(ctx context.Context, source *pgx.Conn, s Sync) (int64, error) {
	var lag *int64
	err := source.QueryRow(ctx, `
		SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn)::bigint
		  FROM pg_replication_slots WHERE slot_name = $1`, s.Name).Scan(&lag)
	if err == pgx.ErrNoRows || lag == nil {
		return 0, fmt.Errorf("logicalrepl: slot %s not found", s.Name)
	}
	if err != nil {
		return 0, err
	}
	return *lag, nil
}

// WaitSynced polls until the initial copy is complete AND lag reaches zero,
// or ctx expires. Poll interval is coarse — this runs for minutes-to-hours on
// real migrations.
func WaitSynced(ctx context.Context, source, target *pgx.Conn, s Sync, poll time.Duration) error {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		done, _, err := InitialCopyDone(ctx, target, s)
		if err != nil {
			return err
		}
		if done {
			lag, err := LagBytes(ctx, source, s)
			if err != nil {
				return err
			}
			if lag == 0 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("logicalrepl: sync wait: %w", ctx.Err())
		case <-time.After(poll):
		}
	}
}

// SyncSequences copies sequence positions source → target; logical
// replication does not carry them (MIGRATION_STRATEGY §2 stage 5). Margin
// adds headroom when the source cannot be fully frozen.
func SyncSequences(ctx context.Context, source, target *pgx.Conn, margin int64) (int, error) {
	rows, err := source.Query(ctx, `
		SELECT format('%I.%I', schemaname, sequencename), COALESCE(last_value, 0)
		  FROM pg_sequences
		 WHERE schemaname NOT IN ('pg_catalog', 'information_schema')`)
	if err != nil {
		return 0, err
	}
	type seq struct {
		name string
		val  int64
	}
	var seqs []seq
	for rows.Next() {
		var s seq
		if err := rows.Scan(&s.name, &s.val); err != nil {
			rows.Close()
			return 0, err
		}
		seqs = append(seqs, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, s := range seqs {
		if s.val == 0 {
			continue // never advanced; setval(0) would be invalid
		}
		// Pass the sequence name as a regclass-cast parameter rather than
		// interpolating it into the statement — the name comes from the
		// source's catalog, so a hostile source could otherwise smuggle SQL
		// through a crafted sequence identifier (audit finding).
		if _, err := target.Exec(ctx, `SELECT setval($1::regclass, $2)`, s.name, s.val+margin); err != nil {
			return 0, fmt.Errorf("setval %s: %w", s.name, err)
		}
	}
	return len(seqs), nil
}

// Cutover tears the replication link down after the freeze-verify-flip
// sequence. Ordering is load-bearing (audit finding): a plain
// DROP SUBSCRIPTION tries to drop the remote slot over the source connection,
// so if the source is unreachable it errors and RETURNS BEFORE the explicit
// slot drop — leaking a slot that retains WAL on the source forever (the RDS
// storage-growth failure mode). Instead we DETACH the slot from the
// subscription first (DISABLE + slot_name = NONE), so DROP SUBSCRIPTION is
// purely local and always succeeds, then drop the now-unowned slot on the
// source ourselves.
func Cutover(ctx context.Context, source, target *pgx.Conn, s Sync) error {
	if err := s.validate(); err != nil {
		return err
	}
	var exists bool
	if err := target.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_subscription WHERE subname = $1)`, s.Name).Scan(&exists); err != nil {
		return fmt.Errorf("check subscription: %w", err)
	}
	if exists {
		if _, err := target.Exec(ctx, `ALTER SUBSCRIPTION `+s.Name+` DISABLE`); err != nil {
			return fmt.Errorf("disable subscription: %w", err)
		}
		if _, err := target.Exec(ctx, `ALTER SUBSCRIPTION `+s.Name+` SET (slot_name = NONE)`); err != nil {
			return fmt.Errorf("detach slot: %w", err)
		}
		if _, err := target.Exec(ctx, `DROP SUBSCRIPTION `+s.Name); err != nil {
			return fmt.Errorf("drop subscription: %w", err)
		}
	}
	// The slot is now unowned; dropping it here is the only thing that frees
	// the WAL it was retaining.
	if _, err := source.Exec(ctx,
		`SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = $1`,
		s.Name); err != nil {
		return fmt.Errorf("drop replication slot: %w", err)
	}
	if _, err := source.Exec(ctx, `DROP PUBLICATION IF EXISTS `+s.Name); err != nil {
		return fmt.Errorf("drop publication: %w", err)
	}
	return nil
}

// Abort is Cutover under a different intent: pre-cutover rollback discards
// the link (the copied target data is discarded by the caller).
func Abort(ctx context.Context, source, target *pgx.Conn, s Sync) error {
	return Cutover(ctx, source, target, s)
}

// DropSourceObjects frees the SOURCE-side replication objects (the
// WAL-retaining slot and the publication). Use it when the target is
// unreachable during cleanup: the subscription cannot be dropped, but the slot
// MUST be, or it retains WAL on the source forever.
func DropSourceObjects(ctx context.Context, source *pgx.Conn, s Sync) error {
	if err := s.validate(); err != nil {
		return err
	}
	if _, err := source.Exec(ctx,
		`SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = $1`,
		s.Name); err != nil {
		return fmt.Errorf("drop replication slot: %w", err)
	}
	if _, err := source.Exec(ctx, `DROP PUBLICATION IF EXISTS `+s.Name); err != nil {
		return fmt.Errorf("drop publication: %w", err)
	}
	return nil
}

func escapeLiteral(v string) string {
	out := []byte{'\''}
	for i := 0; i < len(v); i++ {
		if v[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, v[i])
	}
	return string(append(out, '\''))
}
