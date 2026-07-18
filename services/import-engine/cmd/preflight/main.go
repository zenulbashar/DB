// Command preflight inspects a source PostgreSQL database and prints the
// migration preflight report as JSON (MIGRATION_STRATEGY §2 stage 1).
//
//	preflight -kind neon "postgres://user:pass@host/db?sslmode=require"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zenulbashar/DB/services/import-engine/internal/preflight"
)

func main() {
	kind := flag.String("kind", "generic", "source kind: neon|supabase|rds|azure|generic")
	timeout := flag.Duration("timeout", 30*time.Second, "overall timeout")
	flag.Parse()

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: preflight [-kind neon|supabase|rds|azure|generic] <connection-url>")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close(context.Background())

	report, err := preflight.Run(ctx, conn, preflight.SourceKind(*kind))
	if err != nil {
		fmt.Fprintf(os.Stderr, "preflight: %v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
	if len(report.Blockers) > 0 {
		os.Exit(3)
	}
}
