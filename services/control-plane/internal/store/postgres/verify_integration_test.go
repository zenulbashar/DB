//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

// TestRestoreVerificationLedger exercises the verify loop's store methods
// against real SQL: due-listing, start/finish, running-listing, and the
// AdminOverview failure count (R-2).
func TestRestoreVerificationLedger(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	pr, err := s.CreateProject(ctx, store.CreateProjectParams{
		OrgID: res.Org.ID, Name: "App", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}
	brID := *pr.DefaultBranchID

	// A provisioning branch is not due (only ready branches archive reliably).
	due, err := s.ListRestoreVerifyDue(ctx, 24*time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("provisioning branch listed as due: %v", due)
	}

	if err := s.MarkBranchReady(ctx, brID); err != nil {
		t.Fatal(err)
	}

	// Never verified → due, with full BranchWork context.
	due, err = s.ListRestoreVerifyDue(ctx, 24*time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Branch.ID != brID || due[0].PGVersion != 17 {
		t.Fatalf("due = %+v, want the ready branch", due)
	}

	// Running → excluded from due, visible in the running list.
	if err := s.StartRestoreVerification(ctx, brID); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.ListRestoreVerifyDue(ctx, 24*time.Hour, 0); len(due) != 0 {
		t.Fatalf("running branch still listed due: %v", due)
	}
	running, err := s.ListRunningRestoreVerifications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0].BranchID != brID || running[0].ProjectID != pr.ID {
		t.Fatalf("running = %+v", running)
	}

	// Fail → counted by AdminOverview; still not due within the window
	// (verified_at is fresh), and no longer running.
	if err := s.FinishRestoreVerification(ctx, brID, false, "clone unhealthy"); err != nil {
		t.Fatal(err)
	}
	if running, _ := s.ListRunningRestoreVerifications(ctx); len(running) != 0 {
		t.Fatalf("finished verification still running: %v", running)
	}
	o, err := s.AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.RestoreVerifyFailures != 1 {
		t.Fatalf("restore_verify_failures = %d, want 1", o.RestoreVerifyFailures)
	}
	if due, _ := s.ListRestoreVerifyDue(ctx, 24*time.Hour, 0); len(due) != 0 {
		t.Fatalf("freshly-failed branch already due again: %v", due)
	}

	// Pass (re-verify) → failure count drops; a zero window makes it due again.
	if err := s.StartRestoreVerification(ctx, brID); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRestoreVerification(ctx, brID, true, ""); err != nil {
		t.Fatal(err)
	}
	o, _ = s.AdminOverview(ctx)
	if o.RestoreVerifyFailures != 0 {
		t.Fatalf("restore_verify_failures after pass = %d, want 0", o.RestoreVerifyFailures)
	}
	if due, _ := s.ListRestoreVerifyDue(ctx, 0, 0); len(due) != 1 {
		t.Fatalf("zero window should make the branch due again, got %v", due)
	}
}
