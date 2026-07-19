package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/zenulbashar/DB/services/control-plane/internal/server"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/memory"
)

const gatewayTok = "test-gateway-token"

func gatewayServer(t *testing.T, token string) http.Handler {
	t.Helper()
	return server.New(memory.New(),
		server.Config{BootstrapToken: bootToken, GatewayToken: token, Version: "test", Keyring: testKeyring(t)},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// provisioningBranch bootstraps + creates a project and returns the handler and
// the (provisioning) default branch id.
func provisioningBranch(t *testing.T, h http.Handler) string {
	t.Helper()
	status, body := do(t, h, "POST", "/v1/bootstrap", "", map[string]any{
		"bootstrap_token": bootToken, "email": "o@example.com", "org_name": "Acme",
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("bootstrap = %d %s", status, body)
	}
	var res struct {
		APIKey struct {
			Token string `json:"token"`
		} `json:"api_key"`
	}
	mustUnmarshal(t, body, &res)
	status, body = do(t, h, "POST", "/v1/projects", res.APIKey.Token, map[string]any{"name": "app"}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create project = %d %s", status, body)
	}
	var created struct {
		Project struct {
			DefaultBranchID *string `json:"default_branch_id"`
		} `json:"project"`
	}
	mustUnmarshal(t, body, &created)
	return *created.Project.DefaultBranchID
}

func TestInternalWakeEndpoint(t *testing.T) {
	h := gatewayServer(t, gatewayTok)
	brID := provisioningBranch(t, h)

	// No bearer → 401.
	if s, _ := do(t, h, "POST", "/internal/branches/"+brID+"/wake", "", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("wake without token = %d, want 401", s)
	}
	// Wrong bearer → 401.
	if s, _ := do(t, h, "POST", "/internal/branches/"+brID+"/wake", "wrong-token", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("wake with wrong token = %d, want 401", s)
	}
	// Correct token but the branch is provisioning (not wakeable) → 409.
	if s, _ := do(t, h, "POST", "/internal/branches/"+brID+"/wake", gatewayTok, nil, nil); s != http.StatusConflict {
		t.Fatalf("wake provisioning branch = %d, want 409", s)
	}
	// Correct token, unknown branch → 404.
	if s, _ := do(t, h, "POST", "/internal/branches/br_doesnotexist/wake", gatewayTok, nil, nil); s != http.StatusNotFound {
		t.Fatalf("wake unknown branch = %d, want 404", s)
	}
}

func TestInternalGatewayActivityEndpoint(t *testing.T) {
	h := gatewayServer(t, gatewayTok)

	// No token → 401.
	if s, _ := do(t, h, "POST", "/internal/gateway-activity", "", map[string]any{
		"gateway_id": "gw-1", "branches": map[string]int{"br_1": 0},
	}, nil); s != http.StatusUnauthorized {
		t.Fatalf("activity without token = %d, want 401", s)
	}
	// Valid token + payload → 204.
	if s, _ := do(t, h, "POST", "/internal/gateway-activity", gatewayTok, map[string]any{
		"gateway_id": "gw-1", "branches": map[string]int{"br_1": 3, "br_2": 0},
	}, nil); s != http.StatusNoContent {
		t.Fatalf("activity report = %d, want 204", s)
	}
	// Missing gateway_id → 400.
	if s, _ := do(t, h, "POST", "/internal/gateway-activity", gatewayTok, map[string]any{
		"branches": map[string]int{"br_1": 0},
	}, nil); s != http.StatusBadRequest {
		t.Fatalf("activity without gateway_id = %d, want 400", s)
	}
}

func TestInternalWakeDisabledWithoutToken(t *testing.T) {
	// With no GatewayToken configured the whole /internal surface rejects
	// everything — a shared secret must be explicitly provisioned.
	h := gatewayServer(t, "")
	if s, _ := do(t, h, "POST", "/internal/branches/br_x/wake", "anything", nil, nil); s != http.StatusUnauthorized {
		t.Fatalf("wake with no configured token = %d, want 401", s)
	}
}
