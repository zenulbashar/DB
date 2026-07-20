package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/server"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/memory"
)

const adminTok = "test-admin-token"

// adminEnv is a bootstrapped platform with the admin surface enabled and a
// handle on the memory store for state forcing.
type adminEnv struct {
	h     http.Handler
	mem   *memory.Store
	token string // tenant owner key
	orgID string
	prjID string
	brID  string // default branch of the seeded project
}

func adminServer(t *testing.T, adminToken string) *adminEnv {
	t.Helper()
	mem := memory.New()
	h := server.New(mem,
		server.Config{BootstrapToken: bootToken, AdminToken: adminToken, Version: "test", Keyring: testKeyring(t)},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	status, body := do(t, h, "POST", "/v1/bootstrap", "", map[string]any{
		"bootstrap_token": bootToken, "email": "o@example.com", "org_name": "Acme",
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", status, body)
	}
	var res struct {
		Org    struct{ ID string }
		APIKey struct{ Token string } `json:"api_key"`
	}
	mustUnmarshal(t, body, &res)

	status, body = do(t, h, "POST", "/v1/projects", res.APIKey.Token, map[string]any{"name": "app"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create project = %d %s", status, body)
	}
	var created struct {
		Project struct {
			ID              string  `json:"id"`
			DefaultBranchID *string `json:"default_branch_id"`
		} `json:"project"`
	}
	mustUnmarshal(t, body, &created)
	return &adminEnv{
		h: h, mem: mem, token: res.APIKey.Token,
		orgID: res.Org.ID, prjID: created.Project.ID, brID: *created.Project.DefaultBranchID,
	}
}

func adminGet(t *testing.T, e *adminEnv, path string) (int, []byte) {
	t.Helper()
	return do(t, e.h, "GET", path, adminTok, nil, nil)
}

func TestAdminAuth(t *testing.T) {
	e := adminServer(t, adminTok)

	// No token / wrong token / a *tenant* key → 401. Tenant keys must never
	// open the operator surface (ADR-018).
	for _, tok := range []string{"", "wrong", e.token} {
		if s, _ := do(t, e.h, "GET", "/v1/admin/overview", tok, nil, nil); s != http.StatusUnauthorized {
			t.Fatalf("admin overview with token %q = %d, want 401", tok, s)
		}
	}
	if s, _ := adminGet(t, e, "/v1/admin/overview"); s != http.StatusOK {
		t.Fatalf("admin overview with admin token = %d, want 200", s)
	}

	// Admin surface disabled entirely when the token is unset — even an empty
	// bearer must not match the empty configured token.
	off := adminServer(t, "")
	if s, _ := do(t, off.h, "GET", "/v1/admin/overview", "", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("disabled admin surface = %d, want 401", s)
	}
}

func TestAdminOverviewAndOrgUsage(t *testing.T) {
	e := adminServer(t, adminTok)

	status, body := adminGet(t, e, "/v1/admin/overview")
	if status != http.StatusOK {
		t.Fatalf("overview = %d %s", status, body)
	}
	var o domain.AdminOverview
	mustUnmarshal(t, body, &o)
	if o.Orgs != 1 || o.Users != 1 || o.Projects != 1 || o.Branches != 1 || o.Endpoints != 2 {
		t.Fatalf("overview counts = %+v", o)
	}
	if o.BranchesByState["provisioning"] != 1 {
		t.Fatalf("branches_by_state = %v", o.BranchesByState)
	}
	// provisioning holds compute: default branch min_cu 0.25 → allocated 0.25.
	if o.AllocatedCU != 0.25 {
		t.Fatalf("allocated_cu = %v, want 0.25", o.AllocatedCU)
	}
	if o.ActiveAPIKeys != 1 {
		t.Fatalf("active keys = %d", o.ActiveAPIKeys)
	}

	status, body = adminGet(t, e, "/v1/admin/orgs")
	if status != http.StatusOK {
		t.Fatalf("admin orgs = %d %s", status, body)
	}
	var orgs struct{ Data []domain.OrgUsage }
	mustUnmarshal(t, body, &orgs)
	if len(orgs.Data) != 1 {
		t.Fatalf("orgs = %d, want 1", len(orgs.Data))
	}
	u := orgs.Data[0]
	if u.Org.Name != "Acme" || u.Members != 1 || u.Projects != 1 || u.Branches != 1 ||
		u.Endpoints != 2 || u.ActiveAPIKeys != 1 || u.AllocatedCU != 0.25 {
		t.Fatalf("org usage = %+v", u)
	}
}

func TestAdminBranchListAndFilter(t *testing.T) {
	e := adminServer(t, adminTok)
	e.mem.TestForceBranchState(e.brID, domain.StateError)

	status, body := adminGet(t, e, "/v1/admin/branches?state=error")
	if status != http.StatusOK {
		t.Fatalf("admin branches = %d %s", status, body)
	}
	var list struct{ Data []domain.AdminBranch }
	mustUnmarshal(t, body, &list)
	if len(list.Data) != 1 {
		t.Fatalf("error branches = %d, want 1", len(list.Data))
	}
	ab := list.Data[0]
	if ab.ID != e.brID || ab.OrgName != "Acme" || ab.ProjectName != "app" || ab.OrgID != e.orgID {
		t.Fatalf("admin branch = %+v", ab)
	}

	if _, body := adminGet(t, e, "/v1/admin/branches?state=ready"); string(body) == "" {
		t.Fatal("expected a body")
	}
	status, body = adminGet(t, e, "/v1/admin/branches?state=ready")
	mustUnmarshal(t, body, &list)
	if status != http.StatusOK || len(list.Data) != 0 {
		t.Fatalf("ready branches = %d/%d, want 200/0", status, len(list.Data))
	}
}

func TestAdminFixActionsDriveTenantStateMachineAndAudit(t *testing.T) {
	e := adminServer(t, adminTok)

	// Provisioning branch: suspend is an illegal transition → 409 (the admin
	// path reuses the tenant state machine, not a bypass).
	if s, _ := do(t, e.h, "POST", "/v1/admin/branches/"+e.brID+"/suspend", adminTok, nil, nil); s != http.StatusConflict {
		t.Fatalf("admin suspend provisioning = %d, want 409", s)
	}

	e.mem.TestForceBranchState(e.brID, domain.StateReady)
	status, body := do(t, e.h, "POST", "/v1/admin/branches/"+e.brID+"/suspend", adminTok, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("admin suspend ready = %d %s", status, body)
	}
	var b branchJSON
	mustUnmarshal(t, body, &b)
	if b.State != "suspending" {
		t.Fatalf("state after admin suspend = %s, want suspending", b.State)
	}

	// Resume from suspended.
	e.mem.TestForceBranchState(e.brID, domain.StateSuspended)
	status, body = do(t, e.h, "POST", "/v1/admin/branches/"+e.brID+"/resume", adminTok, nil, nil)
	mustUnmarshal(t, body, &b)
	if status != http.StatusOK || b.State != "resuming" {
		t.Fatalf("admin resume = %d state=%s, want 200 resuming", status, b.State)
	}

	// Resize needs a valid cu.
	e.mem.TestForceBranchState(e.brID, domain.StateReady)
	if s, _ := do(t, e.h, "POST", "/v1/admin/branches/"+e.brID+"/resize", adminTok, map[string]any{}, nil); s != http.StatusBadRequest {
		t.Fatalf("admin resize without cu = %d, want 400", s)
	}
	status, body = do(t, e.h, "POST", "/v1/admin/branches/"+e.brID+"/resize", adminTok, map[string]any{"cu": 1.0}, nil)
	mustUnmarshal(t, body, &b)
	if status != http.StatusOK || b.State != "resizing" {
		t.Fatalf("admin resize = %d state=%s, want 200 resizing", status, b.State)
	}

	// Unknown branch → 404.
	if s, _ := do(t, e.h, "POST", "/v1/admin/branches/br_nope/suspend", adminTok, nil, nil); s != http.StatusNotFound {
		t.Fatalf("admin suspend missing branch = %d, want 404", s)
	}

	// Operator interventions are tenant-visible: the org's own audit log shows
	// system/platform_admin entries for each action.
	status, body = do(t, e.h, "GET", "/v1/orgs/"+e.orgID+"/audit-log?limit=20", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("tenant audit = %d %s", status, body)
	}
	var audit struct {
		Data []struct {
			ActorType string `json:"actor_type"`
			ActorID   string `json:"actor_id"`
			Action    string `json:"action"`
			TargetID  string `json:"target_id"`
		} `json:"data"`
	}
	mustUnmarshal(t, body, &audit)
	found := map[string]bool{}
	for _, entry := range audit.Data {
		if entry.ActorType == "system" && entry.ActorID == "platform_admin" && entry.TargetID == e.brID {
			found[entry.Action] = true
		}
	}
	for _, action := range []string{"branch.suspend", "branch.resume", "branch.resize"} {
		if !found[action] {
			t.Fatalf("tenant audit log missing operator action %s (found %v)", action, found)
		}
	}

	// And the admin's cross-org audit feed sees them too.
	status, body = adminGet(t, e, "/v1/admin/audit-log?limit=10")
	if status != http.StatusOK {
		t.Fatalf("admin audit = %d %s", status, body)
	}
	mustUnmarshal(t, body, &audit)
	if len(audit.Data) == 0 {
		t.Fatal("admin audit feed empty")
	}
}
