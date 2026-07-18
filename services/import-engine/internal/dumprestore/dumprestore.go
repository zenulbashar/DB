// Package dumprestore executes the dump_restore migration mode
// (MIGRATION_STRATEGY §2): pg_dump custom-format archive → pg_restore into
// the target, under a caller-managed write freeze. Roles/ownership are
// deliberately not carried — target roles are NimbusDB-managed.
package dumprestore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

type Options struct {
	SourceURL string
	TargetURL string
	// BinDir pins the Postgres client binaries; empty resolves from PATH.
	// The pg_dump major version must be >= the source server's major
	// (dump-binaries-match-source rule, MIGRATION_STRATEGY §4).
	BinDir string
	// Jobs enables parallel restore when > 1.
	Jobs int
	// WorkDir holds the intermediate archive; empty uses a temp dir.
	WorkDir string
}

type Result struct {
	ArchiveBytes  int64
	DumpStderr    string
	RestoreStderr string
}

func (o Options) bin(name string) string {
	if o.BinDir == "" {
		return name
	}
	return filepath.Join(o.BinDir, name)
}

// Run performs dump → restore. The caller owns the freeze window and the
// post-run verification (verify package).
func Run(ctx context.Context, o Options) (*Result, error) {
	workDir := o.WorkDir
	if workDir == "" {
		d, err := os.MkdirTemp("", "ndb-import-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(d)
		workDir = d
	}
	archive := filepath.Join(workDir, "dump.pgcustom")
	res := &Result{}

	dump := exec.CommandContext(ctx, o.bin("pg_dump"),
		"--format=custom",
		"--no-owner", "--no-privileges",
		"--file="+archive,
		o.SourceURL,
	)
	var dumpErr bytes.Buffer
	dump.Stderr = &dumpErr
	if err := dump.Run(); err != nil {
		return nil, fmt.Errorf("pg_dump: %w: %s", err, dumpErr.String())
	}
	res.DumpStderr = dumpErr.String()

	if st, err := os.Stat(archive); err == nil {
		res.ArchiveBytes = st.Size()
	}

	args := []string{
		"--no-owner", "--no-privileges",
		"--exit-on-error",
		"--dbname=" + o.TargetURL,
	}
	if o.Jobs > 1 {
		args = append(args, "--jobs="+strconv.Itoa(o.Jobs))
	}
	args = append(args, archive)
	restore := exec.CommandContext(ctx, o.bin("pg_restore"), args...)
	var restoreErr bytes.Buffer
	restore.Stderr = &restoreErr
	if err := restore.Run(); err != nil {
		return nil, fmt.Errorf("pg_restore: %w: %s", err, restoreErr.String())
	}
	res.RestoreStderr = restoreErr.String()
	return res, nil
}
