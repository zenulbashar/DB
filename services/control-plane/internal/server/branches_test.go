package server_test

import (
	"net/http"
	"strings"
	"testing"
)

type branchJSON struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Role    string  `json:"role"`
	State   string  `json:"state"`
	Parent  *string `json:"parent"`
	Compute struct {
		MinCU           float64 `json:"min_cu"`
		MaxCU           float64 `json:"max_cu"`
		SuspendTimeoutS int     `json:"suspend_timeout_s"`
	} `json:"compute"`
	Endpoints []struct {
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		Host  string `json:"host"`
		State string `json:"state"`
	} `json:"endpoints"`
}

func createProject(t *testing.T, e *env, name string) (id, defaultBranch string) {
	t.Helper()
	status, body := do(t, e.h, "POST", "/v1/projects", e.token, map[string]any{"name": name}, nil)
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
	if created.Project.DefaultBranchID == nil {
		t.Fatal("project must be created with a default branch")
	}
	return created.Project.ID, *created.Project.DefaultBranchID
}

func TestProjectCreatesDefaultBranchWithEndpoints(t *testing.T) {
	e := bootstrapped(t)
	_, brID := createProject(t, e, "roster")

	status, body := do(t, e.h, "GET", "/v1/branches/"+brID, e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("get branch = %d %s", status, body)
	}
	var b branchJSON
	mustUnmarshal(t, body, &b)
	if b.Name != "main" || b.Role != "production" || b.State != "provisioning" {
		t.Fatalf("unexpected default branch: %+v", b)
	}
	if b.Compute.MinCU != 0.25 || b.Compute.MaxCU != 2 || b.Compute.SuspendTimeoutS != 300 {
		t.Fatalf("unexpected compute defaults: %+v", b.Compute)
	}
	if len(b.Endpoints) != 2 {
		t.Fatalf("want 2 endpoints (rw_direct, rw_pooled), got %d", len(b.Endpoints))
	}
	kinds := map[string]string{}
	for _, ep := range b.Endpoints {
		kinds[ep.Kind] = ep.Host
	}
	for _, kind := range []string{"rw_direct", "rw_pooled"} {
		host, ok := kinds[kind]
		if !ok {
			t.Fatalf("missing endpoint kind %s", kind)
		}
		if want := ".syd1.db.nimbus.app"; len(host) < len(want) || host[len(host)-len(want):] != want {
			t.Fatalf("endpoint host %q not in syd1 namespace", host)
		}
	}
}

func TestBranchLifecycle(t *testing.T) {
	e := bootstrapped(t)
	prjID, defBr := createProject(t, e, "p2e")

	// Create a preview branch (parent defaults to main).
	status, body := do(t, e.h, "POST", "/v1/projects/"+prjID+"/branches", e.token,
		map[string]any{"name": "preview-42", "role": "preview"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create branch = %d %s", status, body)
	}
	var b branchJSON
	mustUnmarshal(t, body, &b)
	if b.Parent == nil || *b.Parent != defBr {
		t.Fatalf("parent = %v, want default branch %s", b.Parent, defBr)
	}

	// Duplicate name → 409.
	status, _ = do(t, e.h, "POST", "/v1/projects/"+prjID+"/branches", e.token,
		map[string]any{"name": "preview-42"}, nil)
	if status != http.StatusConflict {
		t.Fatalf("duplicate branch = %d, want 409", status)
	}

	// List shows both.
	status, body = do(t, e.h, "GET", "/v1/projects/"+prjID+"/branches", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("list = %d", status)
	}
	var list struct {
		Data []branchJSON `json:"data"`
	}
	mustUnmarshal(t, body, &list)
	if len(list.Data) != 2 {
		t.Fatalf("want 2 branches, got %d", len(list.Data))
	}

	// Patch compute bounds.
	status, body = do(t, e.h, "PATCH", "/v1/branches/"+b.ID, e.token,
		map[string]any{"compute_max_cu": 4, "suspend_timeout_s": 0}, nil)
	if status != http.StatusOK {
		t.Fatalf("patch = %d %s", status, body)
	}
	var patched branchJSON
	mustUnmarshal(t, body, &patched)
	if patched.Compute.MaxCU != 4 {
		t.Fatalf("max_cu = %v, want 4", patched.Compute.MaxCU)
	}

	// Endpoints listing.
	status, body = do(t, e.h, "GET", "/v1/branches/"+b.ID+"/endpoints", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("endpoints = %d", status)
	}

	// Delete branch; default branch stays protected.
	status, _ = do(t, e.h, "DELETE", "/v1/branches/"+b.ID, e.token, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete = %d", status)
	}
	status, _ = do(t, e.h, "DELETE", "/v1/branches/"+defBr, e.token, nil, nil)
	if status != http.StatusConflict {
		t.Fatalf("delete default branch = %d, want 409", status)
	}

	// Project delete cascades even to the default branch.
	status, _ = do(t, e.h, "DELETE", "/v1/projects/"+prjID, e.token, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete project = %d", status)
	}
	status, _ = do(t, e.h, "GET", "/v1/branches/"+defBr, e.token, nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("default branch after project delete = %d, want 404", status)
	}
}

func TestBranchSuspendResumeWiring(t *testing.T) {
	e := bootstrapped(t)
	_, defBr := createProject(t, e, "wakeapp")

	// A freshly-created branch is provisioning (no reconciler in this test), so
	// suspend is an illegal transition → 409. This exercises the route, scope,
	// and error mapping end-to-end.
	status, body := do(t, e.h, "POST", "/v1/branches/"+defBr+"/suspend", e.token, nil, nil)
	if status != http.StatusConflict {
		t.Fatalf("suspend provisioning branch = %d, want 409 (%s)", status, body)
	}
	var prob struct {
		Type string `json:"type"`
	}
	mustUnmarshal(t, body, &prob)
	if !strings.HasSuffix(prob.Type, "illegal-state-transition") {
		t.Fatalf("problem type = %q, want …/illegal-state-transition", prob.Type)
	}
	// Resume of a provisioning branch is likewise a conflict.
	if status, _ = do(t, e.h, "POST", "/v1/branches/"+defBr+"/resume", e.token, nil, nil); status != http.StatusConflict {
		t.Fatalf("resume provisioning branch = %d, want 409", status)
	}

	// suspend/resume require branches:write.
	status, body = do(t, e.h, "POST", "/v1/orgs/"+e.orgID+"/api-keys", e.token, map[string]any{
		"name": "ro", "scopes": []string{"branches:read"},
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create key = %d %s", status, body)
	}
	var key struct {
		Token string `json:"token"`
	}
	mustUnmarshal(t, body, &key)
	if status, _ = do(t, e.h, "POST", "/v1/branches/"+defBr+"/suspend", key.Token, nil, nil); status != http.StatusForbidden {
		t.Fatalf("suspend with branches:read = %d, want 403", status)
	}

	// Unknown branch → 404.
	if status, _ = do(t, e.h, "POST", "/v1/branches/br_nope/suspend", e.token, nil, nil); status != http.StatusNotFound {
		t.Fatalf("suspend unknown branch = %d, want 404", status)
	}
}

func TestBranchValidation(t *testing.T) {
	e := bootstrapped(t)
	prjID, _ := createProject(t, e, "app")

	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{
		"empty name":     {map[string]any{"name": ""}, http.StatusBadRequest},
		"bad role":       {map[string]any{"name": "x", "role": "staging"}, http.StatusBadRequest},
		"cu too small":   {map[string]any{"name": "x", "compute_min_cu": 0.1}, http.StatusBadRequest},
		"cu too big":     {map[string]any{"name": "x", "compute_max_cu": 32}, http.StatusBadRequest},
		"min above max":  {map[string]any{"name": "x", "compute_min_cu": 4.0, "compute_max_cu": 1.0}, http.StatusBadRequest},
		"unknown parent": {map[string]any{"name": "x", "from_branch": "br_nope"}, http.StatusNotFound},
		"valid custom":   {map[string]any{"name": "dev", "compute_min_cu": 0.5, "compute_max_cu": 4}, http.StatusCreated},
	} {
		status, body := do(t, e.h, "POST", "/v1/projects/"+prjID+"/branches", e.token, tc.body, nil)
		if status != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, status, tc.want, body)
		}
	}
}

func TestBranchScopes(t *testing.T) {
	e := bootstrapped(t)
	prjID, defBr := createProject(t, e, "scoped")

	// branches:read only → can list, cannot create.
	status, body := do(t, e.h, "POST", "/v1/orgs/"+e.orgID+"/api-keys", e.token, map[string]any{
		"name": "ro", "scopes": []string{"branches:read"},
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create key = %d", status)
	}
	var key struct {
		Token string `json:"token"`
	}
	mustUnmarshal(t, body, &key)

	if status, _ = do(t, e.h, "GET", "/v1/branches/"+defBr, key.Token, nil, nil); status != http.StatusOK {
		t.Fatalf("read with branches:read = %d", status)
	}
	if status, _ = do(t, e.h, "POST", "/v1/projects/"+prjID+"/branches", key.Token,
		map[string]any{"name": "nope"}, nil); status != http.StatusForbidden {
		t.Fatalf("create with branches:read = %d, want 403", status)
	}
	// endpoints need endpoints:read.
	if status, _ = do(t, e.h, "GET", "/v1/branches/"+defBr+"/endpoints", key.Token, nil, nil); status != http.StatusForbidden {
		t.Fatalf("endpoints with branches:read = %d, want 403", status)
	}
}
