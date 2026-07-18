package domain

import "testing"

func TestImportTransitions(t *testing.T) {
	cases := []struct {
		mode ImportMode
		from ImportState
		to   ImportState
		ok   bool
	}{
		// Happy path, dump_restore (skips the sync states).
		{ImportDumpRestore, ImportPending, ImportPreflight, true},
		{ImportDumpRestore, ImportPreflight, ImportSchemaCopy, true},
		{ImportDumpRestore, ImportSchemaCopy, ImportCutoverReady, true},
		{ImportDumpRestore, ImportSchemaCopy, ImportInitialCopy, false}, // wrong mode
		{ImportDumpRestore, ImportCutoverReady, ImportCutOver, true},
		{ImportDumpRestore, ImportCutOver, ImportVerified, true},

		// Happy path, logical_replication (walks the full sync chain).
		{ImportLogicalRepl, ImportSchemaCopy, ImportInitialCopy, true},
		{ImportLogicalRepl, ImportSchemaCopy, ImportCutoverReady, false}, // wrong mode
		{ImportLogicalRepl, ImportInitialCopy, ImportLiveSync, true},
		{ImportLogicalRepl, ImportLiveSync, ImportCutoverReady, true},

		// No skipping, no reversing.
		{ImportLogicalRepl, ImportPending, ImportCutOver, false},
		{ImportLogicalRepl, ImportLiveSync, ImportVerified, false},
		{ImportLogicalRepl, ImportCutoverReady, ImportLiveSync, false},

		// No self-edges: the runner must STAY (return no-progress) while
		// waiting, never issue a same-state transition. A live_sync -> live_sync
		// self-transition once failed every logical migration on any lag.
		{ImportLogicalRepl, ImportLiveSync, ImportLiveSync, false},
		{ImportLogicalRepl, ImportInitialCopy, ImportInitialCopy, false},

		// Failure/abort from non-terminal; cut_over may fail but not abort.
		{ImportLogicalRepl, ImportLiveSync, ImportFailed, true},
		{ImportLogicalRepl, ImportLiveSync, ImportAborted, true},
		{ImportLogicalRepl, ImportCutOver, ImportFailed, true},
		{ImportLogicalRepl, ImportCutOver, ImportAborted, false},

		// Terminal states are terminal.
		{ImportLogicalRepl, ImportVerified, ImportPending, false},
		{ImportLogicalRepl, ImportFailed, ImportPreflight, false},
		{ImportLogicalRepl, ImportAborted, ImportPreflight, false},
	}
	for _, tc := range cases {
		if got := CanTransition(tc.mode, tc.from, tc.to); got != tc.ok {
			t.Errorf("CanTransition(%s, %s → %s) = %v, want %v", tc.mode, tc.from, tc.to, got, tc.ok)
		}
	}
}
