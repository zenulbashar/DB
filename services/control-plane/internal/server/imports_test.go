package server_test

import (
	"net/http"
	"strings"
	"testing"
)

const sourceURL = "postgres://owner:hunter2@ep-old.ap-southeast-2.aws.neon.tech/neondb?sslmode=require"

func createImport(t *testing.T, e *env, prjID, mode string) string {
	t.Helper()
	status, body := do(t, e.h, "POST", "/v1/projects/"+prjID+"/imports", e.token, map[string]any{
		"source_kind": "neon", "mode": mode, "source_url": sourceURL,
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create import = %d %s", status, body)
	}
	var im struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	mustUnmarshal(t, body, &im)
	if im.State != "pending" {
		t.Fatalf("state = %s", im.State)
	}
	if strings.Contains(string(body), "hunter2") || strings.Contains(string(body), "neon.tech") {
		t.Fatalf("create response leaks source credentials: %s", body)
	}
	return im.ID
}

func TestImportLifecycleDumpRestore(t *testing.T) {
	e := bootstrapped(t)
	prjID, _ := createProject(t, e, "roster")
	impID := createImport(t, e, prjID, "dump_restore")

	// Cutover before ready → 409.
	status, _ := do(t, e.h, "POST", "/v1/imports/"+impID+"/cutover", e.token, nil, nil)
	if status != http.StatusConflict {
		t.Fatalf("early cutover = %d, want 409", status)
	}

	// Runner walks the machine.
	steps := []map[string]any{
		{"state": "preflight"},
		{"state": "schema_copy", "report": map[string]any{"recommended_mode": "dump_restore"}},
		{"state": "cutover_ready", "checkpoints": map[string]any{"archive_bytes": 12345}},
	}
	for _, step := range steps {
		status, body := do(t, e.h, "PATCH", "/v1/imports/"+impID+"/state", e.token, step, nil)
		if status != http.StatusOK {
			t.Fatalf("transition %v = %d %s", step["state"], status, body)
		}
	}

	// Illegal skip is rejected (dump mode never enters live_sync).
	status, _ = do(t, e.h, "PATCH", "/v1/imports/"+impID+"/state", e.token,
		map[string]any{"state": "live_sync"}, nil)
	if status != http.StatusConflict {
		t.Fatalf("illegal transition = %d, want 409", status)
	}

	// Human gate → cut_over → verified; terminal thereafter.
	if status, _ = do(t, e.h, "POST", "/v1/imports/"+impID+"/cutover", e.token, nil, nil); status != http.StatusOK {
		t.Fatalf("cutover = %d", status)
	}
	if status, _ = do(t, e.h, "PATCH", "/v1/imports/"+impID+"/state", e.token,
		map[string]any{"state": "verified", "checkpoints": map[string]any{"checksum_ok": true}}, nil); status != http.StatusOK {
		t.Fatalf("verify = %d", status)
	}
	if status, _ = do(t, e.h, "POST", "/v1/imports/"+impID+"/abort", e.token, nil, nil); status != http.StatusConflict {
		t.Fatalf("abort after verified = %d, want 409", status)
	}

	// State + merged checkpoints visible on read; source URL never is.
	status, body := do(t, e.h, "GET", "/v1/imports/"+impID, e.token, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("get = %d", status)
	}
	if !strings.Contains(string(body), `"verified"`) ||
		!strings.Contains(string(body), "archive_bytes") ||
		!strings.Contains(string(body), "checksum_ok") {
		t.Fatalf("read state incomplete: %s", body)
	}
	if strings.Contains(string(body), "hunter2") {
		t.Fatalf("import read leaks credentials: %s", body)
	}
}

func TestImportAbortAndValidation(t *testing.T) {
	e := bootstrapped(t)
	prjID, _ := createProject(t, e, "p2e")

	impID := createImport(t, e, prjID, "logical_replication")
	status, body := do(t, e.h, "POST", "/v1/imports/"+impID+"/abort", e.token, nil, nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"aborted"`) {
		t.Fatalf("abort = %d %s", status, body)
	}

	for name, req := range map[string]map[string]any{
		"bad kind": {"source_kind": "oracle", "mode": "dump_restore", "source_url": sourceURL},
		"bad mode": {"source_kind": "neon", "mode": "rsync", "source_url": sourceURL},
		"bad url":  {"source_kind": "neon", "mode": "dump_restore", "source_url": "mysql://nope"},
	} {
		status, _ := do(t, e.h, "POST", "/v1/projects/"+prjID+"/imports", e.token, req, nil)
		if status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, status)
		}
	}

	// imports:read cannot mutate.
	status, body = do(t, e.h, "POST", "/v1/orgs/"+e.orgID+"/api-keys", e.token, map[string]any{
		"name": "ro", "scopes": []string{"imports:read"},
	}, nil)
	if status != http.StatusCreated {
		t.Fatal("key create failed")
	}
	var key struct {
		Token string `json:"token"`
	}
	mustUnmarshal(t, body, &key)
	if status, _ = do(t, e.h, "GET", "/v1/imports/"+impID, key.Token, nil, nil); status != http.StatusOK {
		t.Fatalf("read with imports:read = %d", status)
	}
	if status, _ = do(t, e.h, "POST", "/v1/imports/"+impID+"/cutover", key.Token, nil, nil); status != http.StatusForbidden {
		t.Fatalf("cutover with imports:read = %d, want 403", status)
	}
}
