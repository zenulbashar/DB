// Package preflight inspects a source PostgreSQL database before migration
// (MIGRATION_STRATEGY §2 stage 1). It produces the written report the console
// renders and the import workflow gates on: capability blockers, remediation
// warnings, and a mode recommendation. Read-only by construction — every
// query touches catalogs and settings only.
package preflight

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type SourceKind string

const (
	SourceNeon     SourceKind = "neon"
	SourceSupabase SourceKind = "supabase"
	SourceRDS      SourceKind = "rds"
	SourceAzure    SourceKind = "azure"
	SourceGeneric  SourceKind = "generic"
)

type Mode string

const (
	ModeDumpRestore Mode = "dump_restore"
	ModeLogicalRepl Mode = "logical_replication"
)

// dumpRestoreCutoverBytes: below this, a write-freeze dump/restore window is
// short enough to prefer over replication setup (MIGRATION_STRATEGY §1).
const dumpRestoreCutoverBytes = 10 << 30 // 10 GiB

type Finding struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

type Extension struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Table struct {
	Schema          string `json:"schema"`
	Name            string `json:"name"`
	EstimatedRows   int64  `json:"estimated_rows"`
	HasPrimaryKey   bool   `json:"has_primary_key"`
	ReplicaIdentity string `json:"replica_identity"` // default|nothing|full|index
}

type Report struct {
	SourceKind          SourceKind  `json:"source_kind"`
	ServerVersion       string      `json:"server_version"`
	VersionNum          int         `json:"version_num"`
	DatabaseSizeBytes   int64       `json:"database_size_bytes"`
	WALLevel            string      `json:"wal_level"`
	MaxReplicationSlots int         `json:"max_replication_slots"`
	CanReplicate        bool        `json:"can_replicate"` // role has REPLICATION or superuser
	Extensions          []Extension `json:"extensions"`
	Tables              []Table     `json:"tables"`
	TablesWithoutPK     []string    `json:"tables_without_pk"`
	Enums               []string    `json:"enums"`
	SequenceCount       int         `json:"sequence_count"`
	RecommendedMode     Mode        `json:"recommended_mode"`
	Blockers            []Finding   `json:"blockers"`
	Warnings            []Finding   `json:"warnings"`
}

// Extensions NimbusDB pre-approves on target branches
// (DATABASE_ARCHITECTURE §8 allowlist).
var extensionAllowlist = map[string]bool{
	"pgcrypto": true, "uuid-ossp": true, "postgis": true, "vector": true,
	"pg_trgm": true, "pg_stat_statements": true, "citext": true, "hstore": true,
	"btree_gin": true, "btree_gist": true, "unaccent": true,
}

// Run inspects the database behind conn and derives findings for the given
// source kind.
func Run(ctx context.Context, conn *pgx.Conn, kind SourceKind) (*Report, error) {
	r := &Report{SourceKind: kind}

	if err := conn.QueryRow(ctx,
		`SELECT current_setting('server_version'),
		        current_setting('server_version_num')::int,
		        pg_database_size(current_database()),
		        current_setting('wal_level'),
		        current_setting('max_replication_slots')::int`).
		Scan(&r.ServerVersion, &r.VersionNum, &r.DatabaseSizeBytes, &r.WALLevel, &r.MaxReplicationSlots); err != nil {
		return nil, fmt.Errorf("preflight: server facts: %w", err)
	}

	if err := conn.QueryRow(ctx,
		`SELECT rolreplication OR rolsuper FROM pg_roles WHERE rolname = current_user`).
		Scan(&r.CanReplicate); err != nil {
		return nil, fmt.Errorf("preflight: role facts: %w", err)
	}

	rows, err := conn.Query(ctx,
		`SELECT extname, extversion FROM pg_extension WHERE extname <> 'plpgsql' ORDER BY extname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e Extension
		if err := rows.Scan(&e.Name, &e.Version); err != nil {
			rows.Close()
			return nil, err
		}
		r.Extensions = append(r.Extensions, e)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}

	rows, err = conn.Query(ctx, `
		SELECT n.nspname, c.relname, GREATEST(c.reltuples::bigint, 0),
		       EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.oid AND i.indisprimary),
		       CASE c.relreplident WHEN 'd' THEN 'default' WHEN 'n' THEN 'nothing'
		                           WHEN 'f' THEN 'full' WHEN 'i' THEN 'index' END
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind = 'r'
		   AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		 ORDER BY n.nspname, c.relname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Schema, &t.Name, &t.EstimatedRows, &t.HasPrimaryKey, &t.ReplicaIdentity); err != nil {
			rows.Close()
			return nil, err
		}
		r.Tables = append(r.Tables, t)
		if !t.HasPrimaryKey && t.ReplicaIdentity != "full" {
			r.TablesWithoutPK = append(r.TablesWithoutPK, t.Schema+"."+t.Name)
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}

	rows, err = conn.Query(ctx, `
		SELECT DISTINCT t.typname
		  FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
		 WHERE t.typtype = 'e' AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		 ORDER BY t.typname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		r.Enums = append(r.Enums, name)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}

	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind = 'S' AND n.nspname NOT IN ('pg_catalog', 'information_schema')`).
		Scan(&r.SequenceCount); err != nil {
		return nil, err
	}

	derive(r)
	return r, nil
}

// derive turns raw facts into blockers, warnings, and the mode recommendation.
func derive(r *Report) {
	if r.DatabaseSizeBytes < dumpRestoreCutoverBytes {
		r.RecommendedMode = ModeDumpRestore
	} else {
		r.RecommendedMode = ModeLogicalRepl
	}

	if r.VersionNum < 100000 {
		r.Blockers = append(r.Blockers, Finding{
			Code:    "version-too-old",
			Message: fmt.Sprintf("PostgreSQL %s does not support logical replication (needs ≥ 10)", r.ServerVersion),
			Remediation: "Only dump_restore mode is available; expect a write-freeze window " +
				"proportional to database size.",
		})
		r.RecommendedMode = ModeDumpRestore
	}

	if r.WALLevel != "logical" {
		r.Warnings = append(r.Warnings, Finding{
			Code:        "wal-level",
			Message:     fmt.Sprintf("wal_level is %q; logical_replication mode requires \"logical\"", r.WALLevel),
			Remediation: walLevelRemediation(r.SourceKind),
		})
	}
	if !r.CanReplicate {
		r.Warnings = append(r.Warnings, Finding{
			Code:        "replication-privilege",
			Message:     "connecting role lacks the REPLICATION privilege",
			Remediation: replicationRemediation(r.SourceKind),
		})
	}
	if len(r.TablesWithoutPK) > 0 {
		r.Warnings = append(r.Warnings, Finding{
			Code: "tables-without-pk",
			Message: fmt.Sprintf("%d table(s) have no primary key and replica identity is not FULL: %s",
				len(r.TablesWithoutPK), strings.Join(r.TablesWithoutPK, ", ")),
			Remediation: "Run ALTER TABLE <t> REPLICA IDENTITY FULL on each before logical replication " +
				"(UPDATE/DELETE rows cannot be replicated otherwise).",
		})
	}

	var offList []string
	for _, e := range r.Extensions {
		if !extensionAllowlist[e.Name] {
			offList = append(offList, e.Name)
		}
	}
	if len(offList) > 0 {
		sort.Strings(offList)
		r.Warnings = append(r.Warnings, Finding{
			Code:        "extensions-review",
			Message:     "extensions outside the NimbusDB allowlist: " + strings.Join(offList, ", "),
			Remediation: "Request allowlisting before cutover, or confirm the application does not need them on the target.",
		})
	}

	if kindNote := sourceNote(r.SourceKind); kindNote != nil {
		r.Warnings = append(r.Warnings, *kindNote)
	}
}

func walLevelRemediation(kind SourceKind) string {
	switch kind {
	case SourceRDS:
		return "Set rds.logical_replication=1 in the DB parameter group and reboot the instance (MIGRATION_STRATEGY §4)."
	case SourceAzure:
		return "Set the wal_level=LOGICAL server parameter on the Flexible Server and restart."
	case SourceNeon, SourceSupabase:
		return "Enable logical replication in the project settings (both platforms expose this toggle)."
	default:
		return "Set wal_level=logical in postgresql.conf and restart PostgreSQL."
	}
}

func replicationRemediation(kind SourceKind) string {
	switch kind {
	case SourceRDS:
		return "GRANT rds_replication TO <user>."
	case SourceAzure:
		return "az postgres flexible-server ad-admin / GRANT azure_pg_admin replication role to the user."
	case SourceNeon:
		return "Use the project owner role on the DIRECT (unpooled) host — the pooled endpoint cannot hold replication slots."
	case SourceSupabase:
		return "Use the postgres role via the session pooler or direct connection."
	default:
		return "ALTER ROLE <user> REPLICATION."
	}
}

// sourceNote returns the standing per-source caveat, independent of findings.
func sourceNote(kind SourceKind) *Finding {
	switch kind {
	case SourceNeon:
		return &Finding{
			Code:        "neon-source",
			Message:     "Neon sources: autosuspend can pause the source mid-sync; replication must use the direct host.",
			Remediation: "Disable autosuspend for the migration window and connect via the unpooled hostname.",
		}
	case SourceSupabase:
		return &Finding{
			Code:        "supabase-source",
			Message:     "Supabase databases carry platform schemas (auth, storage, realtime, …); Supabase Auth features do not transfer.",
			Remediation: "Default import scope is application schemas; explicitly opt in to copying auth data as plain tables (MIGRATION_STRATEGY §4).",
		}
	case SourceRDS:
		return &Finding{
			Code:        "rds-source",
			Message:     "Long-lived replication slots grow storage on RDS until dropped.",
			Remediation: "Monitor free storage during live-sync; abort drops the slot automatically.",
		}
	default:
		return nil
	}
}
