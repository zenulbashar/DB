package memory

import (
	"context"
	"testing"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

// seedReadyBranch injects a ready branch with two ready endpoints directly (the
// reconciler that would normally flip provisioning→ready is not part of a
// store-level test).
func seedReadyBranch(s *Store, orgID, brID string) {
	s.branches[brID] = domain.Branch{
		ID: brID, OrgID: orgID, ProjectID: "prj_1", Name: "main",
		Role: domain.BranchProduction, State: domain.StateReady,
		Compute: domain.Compute{MinCU: 0.25, MaxCU: 2, SuspendTimeoutS: 300},
	}
	s.endpoints["ep_d_"+brID] = domain.Endpoint{ID: "ep_d_" + brID, BranchID: brID, OrgID: orgID, Kind: domain.EndpointRWDirect, State: domain.StateReady}
	s.endpoints["ep_p_"+brID] = domain.Endpoint{ID: "ep_p_" + brID, BranchID: brID, OrgID: orgID, Kind: domain.EndpointRWPooled, State: domain.StateReady}
}

func (s *Store) forceState(brID string, st domain.ResourceState) {
	b := s.branches[brID]
	b.State = st
	s.branches[brID] = b
	for id, ep := range s.endpoints {
		if ep.BranchID == brID {
			ep.State = st
			s.endpoints[id] = ep
		}
	}
}

func endpointStates(s *Store, brID string) []domain.ResourceState {
	var out []domain.ResourceState
	for _, ep := range s.endpoints {
		if ep.BranchID == brID {
			out = append(out, ep.State)
		}
	}
	return out
}

func TestSuspendResumeStateMachine(t *testing.T) {
	s := New()
	ctx := context.Background()
	seedReadyBranch(s, "org_1", "br_1")

	// ready → suspending, endpoints in lockstep.
	b, err := s.SuspendBranch(ctx, "org_1", "br_1")
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if b.State != domain.StateSuspending {
		t.Fatalf("branch state = %s, want suspending", b.State)
	}
	// The transition response carries endpoints (mirrors the postgres store).
	if len(b.Endpoints) != 2 {
		t.Fatalf("suspend response endpoints = %d, want 2", len(b.Endpoints))
	}
	for _, ep := range b.Endpoints {
		if ep.State != domain.StateSuspending {
			t.Fatalf("response endpoint state = %s, want suspending", ep.State)
		}
	}
	for _, st := range endpointStates(s, "br_1") {
		if st != domain.StateSuspending {
			t.Fatalf("endpoint state = %s, want suspending", st)
		}
	}

	// Idempotent: suspend an already-suspending branch is a no-op success.
	if _, err := s.SuspendBranch(ctx, "org_1", "br_1"); err != nil {
		t.Fatalf("idempotent suspend = %v, want success", err)
	}

	// Resume is illegal from suspending (must be suspended first).
	if _, err := s.ResumeBranch(ctx, "org_1", "br_1"); err != store.ErrConflict {
		t.Fatalf("resume from suspending = %v, want conflict", err)
	}

	// Reconciler completes hibernation.
	s.forceState("br_1", domain.StateSuspended)

	// suspended → resuming, endpoints in lockstep.
	b, err = s.ResumeBranch(ctx, "org_1", "br_1")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if b.State != domain.StateResuming {
		t.Fatalf("branch state = %s, want resuming", b.State)
	}
	for _, st := range endpointStates(s, "br_1") {
		if st != domain.StateResuming {
			t.Fatalf("endpoint state = %s, want resuming", st)
		}
	}

	// Idempotent resume; and a resume of an already-ready branch is also a no-op.
	if _, err := s.ResumeBranch(ctx, "org_1", "br_1"); err != nil {
		t.Fatalf("idempotent resume = %v, want success", err)
	}
	s.forceState("br_1", domain.StateReady)
	if _, err := s.ResumeBranch(ctx, "org_1", "br_1"); err != nil {
		t.Fatalf("resume of ready branch = %v, want no-op success", err)
	}
}

func TestWakeBranchByID(t *testing.T) {
	s := New()
	ctx := context.Background()
	seedReadyBranch(s, "org_1", "br_1")
	s.forceState("br_1", domain.StateSuspended)

	// Privileged, org-less wake flips suspended → resuming (endpoints too).
	b, err := s.WakeBranchByID(ctx, "br_1")
	if err != nil {
		t.Fatalf("wake: %v", err)
	}
	if b.State != domain.StateResuming {
		t.Fatalf("branch state = %s, want resuming", b.State)
	}
	for _, st := range endpointStates(s, "br_1") {
		if st != domain.StateResuming {
			t.Fatalf("endpoint state = %s, want resuming", st)
		}
	}
	// Idempotent (already resuming).
	if _, err := s.WakeBranchByID(ctx, "br_1"); err != nil {
		t.Fatalf("idempotent wake = %v, want success", err)
	}
	// Unknown branch is 404.
	if _, err := s.WakeBranchByID(ctx, "br_missing"); err != store.ErrNotFound {
		t.Fatalf("wake missing = %v, want not found", err)
	}
}

func TestSuspendResumeGuards(t *testing.T) {
	s := New()
	ctx := context.Background()
	seedReadyBranch(s, "org_1", "br_1")

	// Cross-org access is 404, never a state change.
	if _, err := s.SuspendBranch(ctx, "org_other", "br_1"); err != store.ErrNotFound {
		t.Fatalf("cross-org suspend = %v, want not found", err)
	}
	if s.branches["br_1"].State != domain.StateReady {
		t.Fatal("cross-org suspend mutated the branch")
	}

	// Suspending a provisioning branch is an illegal transition.
	s.forceState("br_1", domain.StateProvisioning)
	if _, err := s.SuspendBranch(ctx, "org_1", "br_1"); err != store.ErrConflict {
		t.Fatalf("suspend provisioning = %v, want conflict", err)
	}

	// Missing branch is 404.
	if _, err := s.ResumeBranch(ctx, "org_1", "br_missing"); err != store.ErrNotFound {
		t.Fatalf("resume missing = %v, want not found", err)
	}
}
