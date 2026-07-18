// Package verify checks source/target parity after a migration
// (MIGRATION_STRATEGY §3 — non-optional): table sets, exact row counts,
// deterministic sampled content checksums, sequence positions, and enum
// definitions. Any mismatch blocks cutover.
package verify

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

type Mismatch struct {
	Kind   string `json:"kind"` // table_set|row_count|checksum|sequence|enum
	Object string `json:"object"`
	Detail string `json:"detail"`
}

type Result struct {
	TablesChecked int        `json:"tables_checked"`
	RowsSource    int64      `json:"rows_source"`
	RowsTarget    int64      `json:"rows_target"`
	Mismatches    []Mismatch `json:"mismatches"`
}

func (r *Result) OK() bool { return len(r.Mismatches) == 0 }

type Options struct {
	// SampleRows caps the per-table checksum sample. The DEFAULT (0) checksums
	// the FULL table — a bounded sample can miss a single-row content diff in
	// any table larger than the cap, which would let corruption through the
	// cutover gate undetected (audit finding). A positive value is an explicit,
	// caller-acknowledged downgrade for very large tables.
	SampleRows int
}

func Run(ctx context.Context, source, target *pgx.Conn, o Options) (*Result, error) {
	res := &Result{}

	srcTables, err := listTables(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("verify: source tables: %w", err)
	}
	tgtTables, err := listTables(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("verify: target tables: %w", err)
	}

	tgtSet := map[string]bool{}
	for _, t := range tgtTables {
		tgtSet[t] = true
	}
	srcSet := map[string]bool{}
	for _, t := range srcTables {
		srcSet[t] = true
		if !tgtSet[t] {
			res.Mismatches = append(res.Mismatches, Mismatch{
				Kind: "table_set", Object: t, Detail: "missing on target",
			})
		}
	}
	for _, t := range tgtTables {
		if !srcSet[t] {
			res.Mismatches = append(res.Mismatches, Mismatch{
				Kind: "table_set", Object: t, Detail: "unexpected extra table on target",
			})
		}
	}

	for _, t := range srcTables {
		if !tgtSet[t] {
			continue
		}
		res.TablesChecked++

		var srcCount, tgtCount int64
		if err := source.QueryRow(ctx, `SELECT count(*) FROM `+t).Scan(&srcCount); err != nil {
			return nil, fmt.Errorf("verify: count %s: %w", t, err)
		}
		if err := target.QueryRow(ctx, `SELECT count(*) FROM `+t).Scan(&tgtCount); err != nil {
			return nil, fmt.Errorf("verify: count %s: %w", t, err)
		}
		res.RowsSource += srcCount
		res.RowsTarget += tgtCount
		if srcCount != tgtCount {
			res.Mismatches = append(res.Mismatches, Mismatch{
				Kind: "row_count", Object: t,
				Detail: fmt.Sprintf("source=%d target=%d", srcCount, tgtCount),
			})
			continue // checksum would only restate the count mismatch
		}

		srcSum, err := sampledChecksum(ctx, source, t, o.SampleRows)
		if err != nil {
			return nil, fmt.Errorf("verify: checksum %s: %w", t, err)
		}
		tgtSum, err := sampledChecksum(ctx, target, t, o.SampleRows)
		if err != nil {
			return nil, fmt.Errorf("verify: checksum %s: %w", t, err)
		}
		if srcSum != tgtSum {
			res.Mismatches = append(res.Mismatches, Mismatch{
				Kind: "checksum", Object: t,
				Detail: fmt.Sprintf("sampled content differs (source=%s target=%s)", srcSum, tgtSum),
			})
		}
	}

	if err := compareSequences(ctx, source, target, res); err != nil {
		return nil, err
	}
	if err := compareEnums(ctx, source, target, res); err != nil {
		return nil, err
	}

	sort.Slice(res.Mismatches, func(i, j int) bool {
		if res.Mismatches[i].Kind != res.Mismatches[j].Kind {
			return res.Mismatches[i].Kind < res.Mismatches[j].Kind
		}
		return res.Mismatches[i].Object < res.Mismatches[j].Object
	})
	return res, nil
}

// listTables returns quoted, schema-qualified user tables, sorted.
func listTables(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `
		SELECT format('%I.%I', n.nspname, c.relname)
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind = 'r' AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		 ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// sampledChecksum hashes each row to text, orders the hashes, and digests the
// aggregate — identical on both sides for identical content, independent of
// physical row order. With sample <= 0 (the default) it hashes EVERY row, so a
// single differing row is always caught; a positive sample bounds it to the N
// smallest hashes (an explicit downgrade — see Options.SampleRows).
func sampledChecksum(ctx context.Context, conn *pgx.Conn, table string, sample int) (string, error) {
	inner := fmt.Sprintf(`SELECT md5(t::text) AS h FROM %s t`, table)
	if sample > 0 {
		inner = fmt.Sprintf(`SELECT md5(t::text) AS h FROM %s t ORDER BY md5(t::text) LIMIT %d`, table, sample)
	}
	var sum *string
	err := conn.QueryRow(ctx,
		`SELECT md5(string_agg(h, '' ORDER BY h)) FROM (`+inner+`) s`).Scan(&sum)
	if err != nil {
		return "", err
	}
	if sum == nil { // empty table
		return "empty", nil
	}
	return *sum, nil
}

// compareSequences: logical replication does not carry sequence positions and
// dump/restore snapshots them; the target must be at or beyond the source
// (MIGRATION_STRATEGY §2 stage 5 "sequence sync").
func compareSequences(ctx context.Context, source, target *pgx.Conn, res *Result) error {
	seqValues := func(conn *pgx.Conn) (map[string]int64, error) {
		rows, err := conn.Query(ctx, `
			SELECT format('%I.%I', schemaname, sequencename), COALESCE(last_value, 0)
			  FROM pg_sequences
			 WHERE schemaname NOT IN ('pg_catalog', 'information_schema')`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]int64{}
		for rows.Next() {
			var name string
			var v int64
			if err := rows.Scan(&name, &v); err != nil {
				return nil, err
			}
			out[name] = v
		}
		return out, rows.Err()
	}
	src, err := seqValues(source)
	if err != nil {
		return fmt.Errorf("verify: source sequences: %w", err)
	}
	tgt, err := seqValues(target)
	if err != nil {
		return fmt.Errorf("verify: target sequences: %w", err)
	}
	for name, sv := range src {
		tv, ok := tgt[name]
		switch {
		case !ok:
			res.Mismatches = append(res.Mismatches, Mismatch{
				Kind: "sequence", Object: name, Detail: "missing on target",
			})
		case tv < sv:
			res.Mismatches = append(res.Mismatches, Mismatch{
				Kind: "sequence", Object: name,
				Detail: fmt.Sprintf("target last_value %d behind source %d (risk of duplicate keys)", tv, sv),
			})
		}
	}
	return nil
}

func compareEnums(ctx context.Context, source, target *pgx.Conn, res *Result) error {
	enumDef := func(conn *pgx.Conn) (map[string]string, error) {
		rows, err := conn.Query(ctx, `
			SELECT t.typname, string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder)
			  FROM pg_type t
			  JOIN pg_enum e ON e.enumtypid = t.oid
			  JOIN pg_namespace n ON n.oid = t.typnamespace
			 WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
			 GROUP BY t.typname`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var name, labels string
			if err := rows.Scan(&name, &labels); err != nil {
				return nil, err
			}
			out[name] = labels
		}
		return out, rows.Err()
	}
	src, err := enumDef(source)
	if err != nil {
		return fmt.Errorf("verify: source enums: %w", err)
	}
	tgt, err := enumDef(target)
	if err != nil {
		return fmt.Errorf("verify: target enums: %w", err)
	}
	for name, labels := range src {
		got, ok := tgt[name]
		if !ok {
			res.Mismatches = append(res.Mismatches, Mismatch{Kind: "enum", Object: name, Detail: "missing on target"})
		} else if got != labels {
			res.Mismatches = append(res.Mismatches, Mismatch{
				Kind: "enum", Object: name,
				Detail: fmt.Sprintf("labels differ: source=[%s] target=[%s]", labels, got),
			})
		}
	}
	return nil
}
