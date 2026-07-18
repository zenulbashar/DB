package domain

import "time"

type ImportSourceKind string

const (
	ImportNeon     ImportSourceKind = "neon"
	ImportSupabase ImportSourceKind = "supabase"
	ImportRDS      ImportSourceKind = "rds"
	ImportAzure    ImportSourceKind = "azure"
	ImportGeneric  ImportSourceKind = "generic"
)

func (k ImportSourceKind) Valid() bool {
	switch k {
	case ImportNeon, ImportSupabase, ImportRDS, ImportAzure, ImportGeneric:
		return true
	}
	return false
}

type ImportMode string

const (
	ImportDumpRestore ImportMode = "dump_restore"
	ImportLogicalRepl ImportMode = "logical_replication"
)

func (m ImportMode) Valid() bool {
	return m == ImportDumpRestore || m == ImportLogicalRepl
}

type ImportState string

const (
	ImportPending      ImportState = "pending"
	ImportPreflight    ImportState = "preflight"
	ImportSchemaCopy   ImportState = "schema_copy"
	ImportInitialCopy  ImportState = "initial_copy"
	ImportLiveSync     ImportState = "live_sync"
	ImportCutoverReady ImportState = "cutover_ready"
	ImportCutOver      ImportState = "cut_over"
	ImportVerified     ImportState = "verified"
	ImportFailed       ImportState = "failed"
	ImportAborted      ImportState = "aborted"
)

func (s ImportState) Terminal() bool {
	return s == ImportVerified || s == ImportFailed || s == ImportAborted
}

// importTransitions is the migration lifecycle (MIGRATION_STRATEGY §2).
// dump_restore short-circuits schema_copy → cutover_ready (the copy runs
// during schema_copy); logical mode walks the full sync path.
var importTransitions = map[ImportState][]ImportState{
	ImportPending:      {ImportPreflight, ImportFailed, ImportAborted},
	ImportPreflight:    {ImportSchemaCopy, ImportFailed, ImportAborted},
	ImportSchemaCopy:   {ImportInitialCopy, ImportCutoverReady, ImportFailed, ImportAborted},
	ImportInitialCopy:  {ImportLiveSync, ImportFailed, ImportAborted},
	ImportLiveSync:     {ImportCutoverReady, ImportFailed, ImportAborted},
	ImportCutoverReady: {ImportCutOver, ImportFailed, ImportAborted},
	ImportCutOver:      {ImportVerified, ImportFailed},
}

// CanTransition reports whether from → to is a legal lifecycle step for the
// given mode (mode narrows the schema_copy fan-out).
func CanTransition(mode ImportMode, from, to ImportState) bool {
	for _, next := range importTransitions[from] {
		if next != to {
			continue
		}
		if from == ImportSchemaCopy {
			if mode == ImportDumpRestore && to == ImportInitialCopy {
				return false
			}
			if mode == ImportLogicalRepl && to == ImportCutoverReady {
				return false
			}
		}
		return true
	}
	return false
}

type Import struct {
	ID             string           `json:"id"`
	ProjectID      string           `json:"project_id"`
	OrgID          string           `json:"-"`
	TargetBranchID *string          `json:"target_branch_id"`
	SourceKind     ImportSourceKind `json:"source_kind"`
	Mode           ImportMode       `json:"mode"`
	State          ImportState      `json:"state"`
	SourceSecretID string           `json:"-"`
	Report         map[string]any   `json:"report,omitempty"`
	Checkpoints    map[string]any   `json:"checkpoints,omitempty"`
	Error          *string          `json:"error"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}
