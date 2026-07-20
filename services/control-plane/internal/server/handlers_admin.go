package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
)

// Platform-operator surface (ADR-018). Reads are privileged cross-tenant
// aggregates; fix actions resolve the branch's org and drive the SAME
// org-scoped state machine tenants use — no new mutation semantics — and are
// audit-logged into the affected tenant's org (actor system/platform_admin) so
// operator interventions are tenant-visible.

// adminActorID identifies operator actions in tenant audit logs.
const adminActorID = "platform_admin"

// authenticateAdmin guards /v1/admin with the shared operator token
// (constant-time compare; surface disabled entirely when the token is unset).
func (s *Server) authenticateAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.cfg.AdminToken == "" ||
			subtle.ConstantTimeCompare([]byte(presented), []byte(s.cfg.AdminToken)) != 1 {
			writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "Unauthorized", "")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	o, err := s.store.AdminOverview(r.Context())
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleAdminOrgs(w http.ResponseWriter, r *http.Request) {
	usage, err := s.store.AdminListOrgUsage(r.Context())
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	if usage == nil {
		usage = []domain.OrgUsage{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": usage})
}

func (s *Server) handleAdminBranches(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	branches, err := s.store.AdminListBranches(r.Context(), state, limit)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	if branches == nil {
		branches = []domain.AdminBranch{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": branches})
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.store.AdminRecentAudit(r.Context(), limit)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	if entries == nil {
		entries = []domain.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": entries})
}

// handleAdminBranchAction is the operator fix path: suspend/resume/resize any
// tenant's branch through the tenant state machine (same 409 semantics).
func (s *Server) handleAdminBranchAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		brID := chi.URLParam(r, "br")
		orgID, err := s.store.ResolveBranchOrg(r.Context(), brID)
		if err != nil {
			s.branchErr(w, r, err)
			return
		}

		var b *domain.Branch
		details := map[string]any{"admin": true}
		switch action {
		case "suspend":
			b, err = s.store.SuspendBranch(r.Context(), orgID, brID)
		case "resume":
			b, err = s.store.ResumeBranch(r.Context(), orgID, brID)
		case "resize":
			var req struct {
				CU float64 `json:"cu"`
			}
			if jerr := json.NewDecoder(r.Body).Decode(&req); jerr != nil || req.CU <= 0 {
				writeProblem(w, r, http.StatusBadRequest, "invalid-body",
					"Invalid request body", "cu (a positive number) is required")
				return
			}
			details["cu"] = req.CU
			b, err = s.store.ResizeBranch(r.Context(), orgID, brID, req.CU)
		}
		if err != nil {
			s.branchErr(w, r, err)
			return
		}
		// Tenant-visible operator audit trail (ADR-018 / SECURITY_MODEL §6).
		s.audit(r, orgID, domain.ActorSystem, adminActorID,
			"branch."+action, "branch", brID, details)
		writeJSON(w, http.StatusOK, b)
	}
}
