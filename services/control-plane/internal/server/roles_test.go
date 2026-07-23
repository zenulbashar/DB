package server_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestProjectSeedsOwnerRoleAndDatabase(t *testing.T) {
	e := bootstrapped(t)
	_, brID := createProject(t, e, "My-App 2")

	status, body := do(t, e.h, "GET", "/v1/branches/"+brID+"/roles", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("list roles = %d %s", status, body)
	}
	var roles struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	mustUnmarshal(t, body, &roles)
	if len(roles.Data) != 1 || roles.Data[0].Name != "my_app_2_owner" {
		t.Fatalf("seeded roles = %+v", roles.Data)
	}

	status, body = do(t, e.h, "GET", "/v1/branches/"+brID+"/databases", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("list databases = %d", status)
	}
	var dbs struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	mustUnmarshal(t, body, &dbs)
	if len(dbs.Data) != 1 || dbs.Data[0].Name != "my_app_2" {
		t.Fatalf("seeded databases = %+v", dbs.Data)
	}
}

func TestRoleLifecycle(t *testing.T) {
	e := bootstrapped(t)
	_, brID := createProject(t, e, "app")

	// Create a role → password shown once.
	status, body := do(t, e.h, "POST", "/v1/branches/"+brID+"/roles", e.token,
		map[string]any{"name": "readonly"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create role = %d %s", status, body)
	}
	var role struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	mustUnmarshal(t, body, &role)
	if role.Password == "" || len(role.Password) != 32 {
		t.Fatalf("expected 32-char password once, got %q", role.Password)
	}

	// Listing never exposes secrets.
	status, body = do(t, e.h, "GET", "/v1/branches/"+brID+"/roles", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatal("list roles failed")
	}
	if strings.Contains(string(body), role.Password) || strings.Contains(string(body), "password") {
		t.Fatalf("role listing leaks secret material: %s", body)
	}

	// Reset issues a fresh password.
	status, body = do(t, e.h, "POST", "/v1/branches/"+brID+"/roles/readonly/reset-password", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("reset = %d %s", status, body)
	}
	var reset struct {
		Password string `json:"password"`
	}
	mustUnmarshal(t, body, &reset)
	if reset.Password == "" || reset.Password == role.Password {
		t.Fatal("reset must mint a new password")
	}

	// Reserved / invalid names rejected.
	for _, bad := range []string{"postgres", "pg_evil", "Bad-Name", ""} {
		status, _ := do(t, e.h, "POST", "/v1/branches/"+brID+"/roles", e.token,
			map[string]any{"name": bad}, nil)
		if status != http.StatusBadRequest {
			t.Errorf("role name %q = %d, want 400", bad, status)
		}
	}

	// Database owned by the role blocks deletion; deleting the db unblocks.
	status, _ = do(t, e.h, "POST", "/v1/branches/"+brID+"/databases", e.token,
		map[string]any{"name": "analytics", "owner_role": "readonly"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create database = %d", status)
	}
	status, _ = do(t, e.h, "DELETE", "/v1/branches/"+brID+"/roles/readonly", e.token, nil, nil)
	if status != http.StatusConflict {
		t.Fatalf("delete owning role = %d, want 409", status)
	}
	status, _ = do(t, e.h, "DELETE", "/v1/branches/"+brID+"/databases/analytics", e.token, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete database = %d", status)
	}
	status, _ = do(t, e.h, "DELETE", "/v1/branches/"+brID+"/roles/readonly", e.token, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete role = %d", status)
	}
}

func TestConnectionURIMaskedAndReveal(t *testing.T) {
	e := bootstrapped(t)
	prjID, _ := createProject(t, e, "shop")

	// Default: masked, pooled endpoint, seeded role + database.
	status, body := do(t, e.h, "GET", "/v1/projects/"+prjID+"/connection-uri", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("connection-uri = %d %s", status, body)
	}
	var res struct {
		URI      string `json:"uri"`
		Role     string `json:"role"`
		Database string `json:"database"`
		Revealed bool   `json:"revealed"`
	}
	mustUnmarshal(t, body, &res)
	if res.Revealed || !strings.Contains(res.URI, "shop_owner:****@") ||
		!strings.HasSuffix(res.URI, "/shop?sslmode=require") ||
		!strings.Contains(res.URI, ".syd1.db.zaleit.com.au") {
		t.Fatalf("masked uri = %+v", res)
	}

	// Reveal with owner key works and returns the real password.
	status, body = do(t, e.h, "GET", "/v1/projects/"+prjID+"/connection-uri?reveal=true", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("reveal = %d %s", status, body)
	}
	mustUnmarshal(t, body, &res)
	if !res.Revealed || strings.Contains(res.URI, "****") {
		t.Fatalf("reveal did not return credentials: %+v", res)
	}

	// A read-only key cannot reveal.
	status, body = do(t, e.h, "POST", "/v1/orgs/"+e.orgID+"/api-keys", e.token, map[string]any{
		"name": "ro", "scopes": []string{"projects:read", "branches:read", "roles:read"},
	}, nil)
	if status != http.StatusCreated {
		t.Fatal("create ro key failed")
	}
	var key struct {
		Token string `json:"token"`
	}
	mustUnmarshal(t, body, &key)
	status, _ = do(t, e.h, "GET", "/v1/projects/"+prjID+"/connection-uri?reveal=true", key.Token, nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("reveal with roles:read = %d, want 403", status)
	}
	// Masked read is fine for roles:read.
	status, _ = do(t, e.h, "GET", "/v1/projects/"+prjID+"/connection-uri", key.Token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("masked with roles:read = %d, want 200", status)
	}

	// Reveals are audited.
	status, body = do(t, e.h, "GET", "/v1/orgs/"+e.orgID+"/audit-log", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatal("audit list failed")
	}
	if !strings.Contains(string(body), "connection_uri.reveal") {
		t.Fatal("credential reveal must be audited")
	}
}
