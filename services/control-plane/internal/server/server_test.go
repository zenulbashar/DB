package server_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zenulbashar/DB/services/control-plane/internal/secrets"
	"github.com/zenulbashar/DB/services/control-plane/internal/server"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/memory"
)

const bootToken = "test-bootstrap-token"

func testKeyring(t *testing.T) *secrets.Keyring {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kr, err := secrets.ParseKeyring("1:"+base64.StdEncoding.EncodeToString(key), "")
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return server.New(memory.New(),
		server.Config{BootstrapToken: bootToken, Version: "test", Keyring: testKeyring(t)},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type env struct {
	t      *testing.T
	h      http.Handler
	orgID  string
	userID string
	token  string // owner key with all scopes
}

// bootstrapped spins up a server and runs the bootstrap flow.
func bootstrapped(t *testing.T) *env {
	t.Helper()
	h := newTestServer(t)
	status, body := do(t, h, "POST", "/v1/bootstrap", "", map[string]any{
		"bootstrap_token": bootToken,
		"email":           "owner@example.com",
		"org_name":        "Acme",
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("bootstrap status = %d, body %s", status, body)
	}
	var res struct {
		Org struct {
			ID string `json:"id"`
		} `json:"org"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		APIKey struct {
			Token string `json:"token"`
		} `json:"api_key"`
	}
	mustUnmarshal(t, body, &res)
	if res.APIKey.Token == "" {
		t.Fatal("bootstrap must return the key secret once")
	}
	return &env{t: t, h: h, orgID: res.Org.ID, userID: res.User.ID, token: res.APIKey.Token}
}

func do(t *testing.T, h http.Handler, method, path, token string, body any, headers map[string]string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func mustUnmarshal(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
}

// --- tests ---

func TestHealth(t *testing.T) {
	status, body := do(t, newTestServer(t), "GET", "/healthz", "", nil, nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"ok"`)) {
		t.Fatalf("healthz = %d %s", status, body)
	}
}

func TestBootstrapOnceOnly(t *testing.T) {
	e := bootstrapped(t)
	status, _ := do(t, e.h, "POST", "/v1/bootstrap", "", map[string]any{
		"bootstrap_token": bootToken, "email": "x@example.com", "org_name": "Second",
	}, nil)
	if status != http.StatusConflict {
		t.Fatalf("second bootstrap = %d, want 409", status)
	}
}

func TestBootstrapRejectsBadToken(t *testing.T) {
	h := newTestServer(t)
	status, _ := do(t, h, "POST", "/v1/bootstrap", "", map[string]any{
		"bootstrap_token": "wrong", "email": "x@example.com", "org_name": "Acme",
	}, nil)
	if status != http.StatusForbidden {
		t.Fatalf("bad token = %d, want 403", status)
	}
}

func TestAuthRequired(t *testing.T) {
	e := bootstrapped(t)
	status, _ := do(t, e.h, "GET", "/v1/orgs", "", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", status)
	}
	status, _ = do(t, e.h, "GET", "/v1/orgs", "ndb_"+fmt.Sprintf("%064x", 0), nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unknown token = %d, want 401", status)
	}
}

func TestScopeEnforcement(t *testing.T) {
	e := bootstrapped(t)
	// Mint a key with read-only project scope, then try to create a project.
	status, body := do(t, e.h, "POST", "/v1/orgs/"+e.orgID+"/api-keys", e.token, map[string]any{
		"name": "ro", "scopes": []string{"projects:read"},
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create key = %d %s", status, body)
	}
	var key struct {
		Token string `json:"token"`
	}
	mustUnmarshal(t, body, &key)

	status, _ = do(t, e.h, "POST", "/v1/projects", key.Token, map[string]any{"name": "nope"}, nil)
	if status != http.StatusForbidden {
		t.Fatalf("out-of-scope create = %d, want 403", status)
	}
	status, _ = do(t, e.h, "GET", "/v1/projects", key.Token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("in-scope list = %d, want 200", status)
	}
}

func TestProjectLifecycleAndAudit(t *testing.T) {
	e := bootstrapped(t)

	status, body := do(t, e.h, "POST", "/v1/projects", e.token, map[string]any{"name": "Prompt2Eat"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create project = %d %s", status, body)
	}
	var created struct {
		Project struct {
			ID        string `json:"id"`
			Slug      string `json:"slug"`
			Region    string `json:"region"`
			PGVersion int    `json:"pg_version"`
			State     string `json:"state"`
		} `json:"project"`
		OwnerRole struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		} `json:"owner_role"`
		Database struct {
			Name string `json:"name"`
		} `json:"database"`
	}
	mustUnmarshal(t, body, &created)
	prj := created.Project
	if prj.Slug != "prompt2eat" || prj.Region != "syd1" || prj.PGVersion != 17 || prj.State != "pending" {
		t.Fatalf("unexpected project defaults: %+v", prj)
	}
	if created.OwnerRole.Name != "prompt2eat_owner" || created.OwnerRole.Password == "" ||
		created.Database.Name != "prompt2eat" {
		t.Fatalf("unexpected seed credentials: %+v", created)
	}

	// Slug collision gets a suffix.
	_, body = do(t, e.h, "POST", "/v1/projects", e.token, map[string]any{"name": "Prompt2Eat X"}, nil)
	var second struct {
		Project struct {
			Slug string `json:"slug"`
		} `json:"project"`
	}
	mustUnmarshal(t, body, &second)
	if second.Project.Slug == prj.Slug {
		t.Fatalf("second project reused slug %q", prj.Slug)
	}

	status, _ = do(t, e.h, "PATCH", "/v1/projects/"+prj.ID, e.token, map[string]any{"name": "P2E"}, nil)
	if status != http.StatusOK {
		t.Fatalf("rename = %d", status)
	}
	status, _ = do(t, e.h, "DELETE", "/v1/projects/"+prj.ID, e.token, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete = %d", status)
	}
	status, _ = do(t, e.h, "GET", "/v1/projects/"+prj.ID, e.token, nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("get deleted = %d, want 404", status)
	}

	// Audit log recorded the mutations.
	status, body = do(t, e.h, "GET", "/v1/orgs/"+e.orgID+"/audit-log", e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("audit list = %d", status)
	}
	var audit struct {
		Data []struct {
			Action string `json:"action"`
		} `json:"data"`
	}
	mustUnmarshal(t, body, &audit)
	actions := map[string]bool{}
	for _, a := range audit.Data {
		actions[a.Action] = true
	}
	for _, want := range []string{"platform.bootstrap", "project.create", "project.update", "project.delete"} {
		if !actions[want] {
			t.Errorf("audit log missing action %q (have %v)", want, actions)
		}
	}
}

func TestCrossOrgIsolationVia404(t *testing.T) {
	e := bootstrapped(t)
	status, _ := do(t, e.h, "GET", "/v1/orgs/org_doesnotexist", e.token, nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("foreign org = %d, want 404", status)
	}
	status, _ = do(t, e.h, "GET", "/v1/projects?org=org_other", e.token, nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("foreign org filter = %d, want 404", status)
	}
}

func TestLastOwnerProtection(t *testing.T) {
	e := bootstrapped(t)

	// Demoting or removing the only owner must fail.
	status, _ := do(t, e.h, "PATCH", "/v1/orgs/"+e.orgID+"/members/"+e.userID, e.token,
		map[string]any{"role": "member"}, nil)
	if status != http.StatusConflict {
		t.Fatalf("demote last owner = %d, want 409", status)
	}
	status, _ = do(t, e.h, "DELETE", "/v1/orgs/"+e.orgID+"/members/"+e.userID, e.token, nil, nil)
	if status != http.StatusConflict {
		t.Fatalf("remove last owner = %d, want 409", status)
	}

	// Add a second owner; then demotion works.
	status, body := do(t, e.h, "POST", "/v1/orgs/"+e.orgID+"/members", e.token,
		map[string]any{"email": "second@example.com", "role": "owner"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("add member = %d %s", status, body)
	}
	status, _ = do(t, e.h, "PATCH", "/v1/orgs/"+e.orgID+"/members/"+e.userID, e.token,
		map[string]any{"role": "member"}, nil)
	if status != http.StatusOK {
		t.Fatalf("demote with second owner = %d, want 200", status)
	}
}

func TestAPIKeyRevocation(t *testing.T) {
	e := bootstrapped(t)
	status, body := do(t, e.h, "POST", "/v1/orgs/"+e.orgID+"/api-keys", e.token, map[string]any{
		"name": "temp", "scopes": []string{"projects:read"},
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create key = %d", status)
	}
	var key struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	mustUnmarshal(t, body, &key)

	status, _ = do(t, e.h, "GET", "/v1/projects", key.Token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("pre-revoke = %d", status)
	}
	status, _ = do(t, e.h, "DELETE", "/v1/orgs/"+e.orgID+"/api-keys/"+key.ID, e.token, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke = %d", status)
	}
	status, _ = do(t, e.h, "GET", "/v1/projects", key.Token, nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("post-revoke = %d, want 401", status)
	}
}

func TestIdempotencyReplay(t *testing.T) {
	e := bootstrapped(t)
	hdr := map[string]string{"Idempotency-Key": "idem-123"}

	status1, body1 := do(t, e.h, "POST", "/v1/projects", e.token, map[string]any{"name": "roster"}, hdr)
	if status1 != http.StatusCreated {
		t.Fatalf("first = %d", status1)
	}
	status2, body2 := do(t, e.h, "POST", "/v1/projects", e.token, map[string]any{"name": "roster"}, hdr)
	if status2 != http.StatusCreated {
		t.Fatalf("replay = %d", status2)
	}
	if !bytes.Equal(body1, body2) {
		t.Fatalf("replay body differs:\n%s\n%s", body1, body2)
	}
	// Without the header a second create makes a distinct project.
	_, body3 := do(t, e.h, "POST", "/v1/projects", e.token, map[string]any{"name": "roster"}, nil)
	if bytes.Equal(body1, body3) {
		t.Fatal("non-idempotent create unexpectedly replayed")
	}
}

func TestValidation(t *testing.T) {
	e := bootstrapped(t)
	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{
		"empty name":  {map[string]any{"name": ""}, http.StatusBadRequest},
		"bad region":  {map[string]any{"name": "x", "region": "us-east-1"}, http.StatusBadRequest},
		"bad version": {map[string]any{"name": "x", "pg_version": 12}, http.StatusBadRequest},
		"valid pg16":  {map[string]any{"name": "x", "pg_version": 16}, http.StatusCreated},
	} {
		status, body := do(t, e.h, "POST", "/v1/projects", e.token, tc.body, nil)
		if status != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, status, tc.want, body)
		}
	}
}
