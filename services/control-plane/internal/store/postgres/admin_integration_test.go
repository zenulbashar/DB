//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

// TestAdminAggregates exercises the platform-operator reads (ADR-018) against
// real SQL: overview counts, per-org usage, branch listing with tenant
// context, cross-org audit, and the branch→org resolver.
func TestAdminAggregates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	res := bootstrapOrg(t, s, "acme")

	pr, err := s.CreateProject(ctx, store.CreateProjectParams{
		OrgID: res.Org.ID, Name: "App", Region: "syd1", PGVersion: 17})
	if err != nil {
		t.Fatal(err)
	}
	brID := *pr.DefaultBranchID
	if err := s.MarkBranchReady(ctx, brID); err != nil {
		t.Fatal(err)
	}

	// Overview: one org/user/project/branch, two endpoints, one active key;
	// a ready branch at default sizing allocates its min_cu (0.25).
	o, err := s.AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.Orgs != 1 || o.Users != 1 || o.Projects != 1 || o.Branches != 1 || o.Endpoints != 2 {
		t.Fatalf("overview = %+v", o)
	}
	if o.BranchesByState["ready"] != 1 || o.AllocatedCU != 0.25 || o.ActiveAPIKeys != 1 {
		t.Fatalf("overview detail = %+v", o)
	}

	// Per-org usage mirrors the same numbers, and last_active_at is set
	// (MarkBranchReady seeds it so a fresh branch gets a full idle window).
	usage, err := s.AdminListOrgUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 {
		t.Fatalf("org usage rows = %d, want 1", len(usage))
	}
	u := usage[0]
	if u.Org.ID != res.Org.ID || u.Members != 1 || u.Projects != 1 || u.Branches != 1 ||
		u.Endpoints != 2 || u.ActiveAPIKeys != 1 || u.AllocatedCU != 0.25 {
		t.Fatalf("org usage = %+v", u)
	}
	if u.LastActiveAt == nil {
		t.Fatal("last_active_at should be seeded by MarkBranchReady")
	}
	if u.BranchesByState["ready"] != 1 {
		t.Fatalf("org branches_by_state = %v", u.BranchesByState)
	}

	// A suspended branch stops counting toward allocated CU.
	if _, err := s.SuspendBranch(ctx, res.Org.ID, brID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkBranchSuspended(ctx, brID); err != nil {
		t.Fatal(err)
	}
	o, err = s.AdminOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if o.AllocatedCU != 0 {
		t.Fatalf("allocated CU after suspend = %v, want 0", o.AllocatedCU)
	}

	// Branch listing carries tenant context and honors the state filter.
	branches, err := s.AdminListBranches(ctx, "suspended", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0].ID != brID ||
		branches[0].OrgName != "acme" || branches[0].ProjectName != "App" {
		t.Fatalf("admin branches = %+v", branches)
	}
	if empty, err := s.AdminListBranches(ctx, "error", 0); err != nil || len(empty) != 0 {
		t.Fatalf("error filter = %v, %v", empty, err)
	}

	// Resolver → org; unknown branch → ErrNotFound.
	orgID, err := s.ResolveBranchOrg(ctx, brID)
	if err != nil || orgID != res.Org.ID {
		t.Fatalf("resolve = %q, %v", orgID, err)
	}
	if _, err := s.ResolveBranchOrg(ctx, "br_nope"); err != store.ErrNotFound {
		t.Fatalf("resolve missing = %v, want ErrNotFound", err)
	}

	// Cross-org audit feed sees entries regardless of org context.
	if err := s.AppendAudit(ctx, domain.AuditEntry{
		ID: "aud_admin_test", OrgID: res.Org.ID, ActorType: domain.ActorSystem,
		ActorID: "platform_admin", Action: "branch.suspend", TargetType: "branch", TargetID: brID,
	}); err != nil {
		t.Fatal(err)
	}
	audit, err := s.AdminRecentAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) == 0 {
		t.Fatal("admin audit feed empty")
	}
}
