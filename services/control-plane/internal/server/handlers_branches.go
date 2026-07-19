package server

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

const (
	minCU        = 0.25
	maxCU        = 8
	maxSuspendS  = 24 * 3600
	maxRetention = 30
)

func (s *Server) handleListBranches(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	prjID := chi.URLParam(r, "prj")
	branches, next, err := s.store.ListBranches(r.Context(), p.OrgID, prjID, pageFrom(r))
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": emptyIfNil(branches), "next_cursor": nullable(next)})
}

func (s *Server) handleCreateBranch(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	prjID := chi.URLParam(r, "prj")
	var req struct {
		Name            string            `json:"name"`
		FromBranch      *string           `json:"from_branch"`
		At              *string           `json:"at"` // RFC3339 PITR fork target
		Role            domain.BranchRole `json:"role"`
		ComputeMinCU    *float64          `json:"compute_min_cu"`
		ComputeMaxCU    *float64          `json:"compute_max_cu"`
		SuspendTimeoutS *int              `json:"suspend_timeout_s"`
	}
	if !decode(w, r, &req) {
		return
	}
	var bootstrapAt *time.Time
	if req.At != nil {
		t, err := time.Parse(time.RFC3339, *req.At)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed",
				"at must be an RFC3339 timestamp.")
			return
		}
		if req.FromBranch == nil {
			writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed",
				"at requires from_branch (point-in-time fork of a parent).")
			return
		}
		utc := t.UTC()
		bootstrapAt = &utc
	}
	// "main" collides with the default branch through the store's per-project
	// uniqueness check, so no special-casing beyond length validation here.
	if req.Name == "" || len(req.Name) > 63 {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "name must be 1–63 characters.")
		return
	}
	if req.Role == "" {
		req.Role = domain.BranchDevelopment
	}
	if !req.Role.Valid() {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "role must be production|preview|development.")
		return
	}
	var compute domain.Compute
	if req.ComputeMinCU != nil {
		compute.MinCU = *req.ComputeMinCU
	}
	if req.ComputeMaxCU != nil {
		compute.MaxCU = *req.ComputeMaxCU
	}
	if req.SuspendTimeoutS != nil {
		compute.SuspendTimeoutS = *req.SuspendTimeoutS
	}
	if msg := validateCompute(req.ComputeMinCU, req.ComputeMaxCU, req.SuspendTimeoutS, nil); msg != "" {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", msg)
		return
	}
	b, err := s.store.CreateBranch(r.Context(), store.CreateBranchParams{
		OrgID: p.OrgID, ProjectID: prjID, Name: req.Name,
		ParentID: req.FromBranch, Role: req.Role, Compute: compute,
		BootstrapAt: bootstrapAt,
	})
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "branch.create", "branch", b.ID,
		map[string]any{"project": prjID, "role": b.Role, "parent": b.ParentID})
	writeJSON(w, http.StatusCreated, b)
}

func (s *Server) handleGetBranch(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	b, err := s.store.GetBranch(r.Context(), p.OrgID, chi.URLParam(r, "br"))
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleUpdateBranch(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brID := chi.URLParam(r, "br")
	var req struct {
		Name            *string  `json:"name"`
		ComputeMinCU    *float64 `json:"compute_min_cu"`
		ComputeMaxCU    *float64 `json:"compute_max_cu"`
		SuspendTimeoutS *int     `json:"suspend_timeout_s"`
		RetentionDays   *int     `json:"retention_days"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name != nil && (*req.Name == "" || len(*req.Name) > 63) {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "name must be 1–63 characters.")
		return
	}
	if msg := validateCompute(req.ComputeMinCU, req.ComputeMaxCU, req.SuspendTimeoutS, req.RetentionDays); msg != "" {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", msg)
		return
	}
	b, err := s.store.UpdateBranch(r.Context(), p.OrgID, brID, store.UpdateBranchParams{
		Name: req.Name, MinCU: req.ComputeMinCU, MaxCU: req.ComputeMaxCU,
		SuspendTimeoutS: req.SuspendTimeoutS, RetentionDays: req.RetentionDays,
	})
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "branch.update", "branch", brID, nil)
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleDeleteBranch(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brID := chi.URLParam(r, "br")
	if err := s.store.SoftDeleteBranch(r.Context(), p.OrgID, brID, time.Now().UTC()); err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "branch.delete", "branch", brID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleSuspendBranch flips a ready branch to suspending (the reconciler
// hibernates the compute). handleResumeBranch flips a suspended branch to
// resuming — the same action the gateway will call for wake-on-connect
// (ADR-014). Both are idempotent, so a repeated call (or a wake storm) is a
// no-op 200 rather than a 409.
func (s *Server) handleSuspendBranch(w http.ResponseWriter, r *http.Request) {
	s.branchComputeAction(w, r, (store.Store).SuspendBranch, "branch.suspend")
}

func (s *Server) handleResumeBranch(w http.ResponseWriter, r *http.Request) {
	s.branchComputeAction(w, r, (store.Store).ResumeBranch, "branch.resume")
}

// handleResizeBranch sets a branch's running compute size (vertical resize,
// ROADMAP Phase 4). Clamped to [min_cu, max_cu] by the store; 409 if the branch
// is not in a resizable (ready/resizing) state.
func (s *Server) handleResizeBranch(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brID := chi.URLParam(r, "br")
	var req struct {
		CU *float64 `json:"cu"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.CU == nil || *req.CU < minCU || *req.CU > maxCU {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed",
			"cu must be between 0.25 and 8.")
		return
	}
	b, err := s.store.ResizeBranch(r.Context(), p.OrgID, brID, *req.CU)
	if err != nil {
		s.branchErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "branch.resize", "branch", brID,
		map[string]any{"cu": b.Compute.CurrentCU, "state": b.State})
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) branchComputeAction(w http.ResponseWriter, r *http.Request,
	action func(store.Store, context.Context, string, string) (*domain.Branch, error), auditAction string) {
	p := principalFrom(r.Context())
	brID := chi.URLParam(r, "br")
	b, err := action(s.store, r.Context(), p.OrgID, brID)
	if err != nil {
		s.branchErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, auditAction, "branch", brID, map[string]any{"state": b.State})
	writeJSON(w, http.StatusOK, b)
}

// authenticateGateway guards the /internal surface with the shared gateway
// token (ADR-014, SECURITY_MODEL §3). When the token is unset the surface is
// disabled entirely; a wrong/missing bearer is 401. Constant-time compared.
func (s *Server) authenticateGateway(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.cfg.GatewayToken == "" ||
			subtle.ConstantTimeCompare([]byte(presented), []byte(s.cfg.GatewayToken)) != 1 {
			writeProblem(w, r, http.StatusUnauthorized, "unauthorized", "Unauthorized", "")
			return
		}
		next(w, r)
	}
}

// handleWakeBranchInternal is the gateway's wake-on-connect trigger: a
// privileged, cross-tenant idempotent flip of a suspended branch to resuming
// (ADR-014). Authenticated by authenticateGateway (no org principal).
func (s *Server) handleWakeBranchInternal(w http.ResponseWriter, r *http.Request) {
	brID := chi.URLParam(r, "br")
	b, err := s.store.WakeBranchByID(r.Context(), brID)
	if err != nil {
		s.branchErr(w, r, err)
		return
	}
	s.audit(r, b.OrgID, domain.ActorSystem, "pg-gateway", "branch.wake", "branch", brID,
		map[string]any{"state": b.State})
	writeJSON(w, http.StatusOK, b)
}

// handleGatewayActivity ingests a gateway replica's per-branch active
// connection counts for the suspend-on-idle decision (ADR-015). Authenticated
// by the gateway token; it is fire-and-forget telemetry, so it returns 204.
func (s *Server) handleGatewayActivity(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GatewayID string         `json:"gateway_id"`
		Branches  map[string]int `json:"branches"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.GatewayID == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "gateway_id is required.")
		return
	}
	if err := s.store.ReportGatewayActivity(r.Context(), req.GatewayID, req.Branches); err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// branchErr maps an illegal compute-state transition to 409 with a clear
// message (a branch that is provisioning/deleting/error cannot be
// suspended/resumed); everything else defers to storeErr.
func (s *Server) branchErr(w http.ResponseWriter, r *http.Request, err error) {
	if err == store.ErrConflict {
		writeProblem(w, r, http.StatusConflict, "illegal-state-transition",
			"Illegal branch state transition",
			"The branch is not in a state that allows this action.")
		return
	}
	s.storeErr(w, r, err)
}

// handleCreateEndpoint adds an endpoint to a branch — in practice a read
// replica (ro_pooled), which the reconciler backs with a hot-standby + a read
// pooler (DATABASE_ARCHITECTURE §5).
func (s *Server) handleCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brID := chi.URLParam(r, "br")
	var req struct {
		Kind domain.EndpointKind `json:"kind"`
	}
	if !decode(w, r, &req) {
		return
	}
	switch req.Kind {
	case domain.EndpointRWDirect, domain.EndpointRWPooled, domain.EndpointROPooled:
	default:
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed",
			"kind must be rw_direct|rw_pooled|ro_pooled.")
		return
	}
	ep, err := s.store.CreateEndpoint(r.Context(), p.OrgID, brID, req.Kind)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "endpoint.create", "endpoint", ep.ID,
		map[string]any{"branch": brID, "kind": ep.Kind})
	writeJSON(w, http.StatusCreated, ep)
}

func (s *Server) handleListEndpoints(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	eps, err := s.store.ListEndpoints(r.Context(), p.OrgID, chi.URLParam(r, "br"))
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": emptyIfNil(eps)})
}

// validateCompute checks the optional compute/retention fields shared by
// branch create and update (DATABASE_ARCHITECTURE §1.1 CU bounds).
func validateCompute(minC, maxC *float64, suspendS, retention *int) string {
	if minC != nil && (*minC < minCU || *minC > maxCU) {
		return "compute_min_cu must be between 0.25 and 8."
	}
	if maxC != nil && (*maxC < minCU || *maxC > maxCU) {
		return "compute_max_cu must be between 0.25 and 8."
	}
	if minC != nil && maxC != nil && *minC > *maxC {
		return "compute_min_cu must not exceed compute_max_cu."
	}
	if suspendS != nil && (*suspendS < 0 || *suspendS > maxSuspendS) {
		return "suspend_timeout_s must be between 0 and 86400."
	}
	if retention != nil && (*retention < 1 || *retention > maxRetention) {
		return "retention_days must be between 1 and 30."
	}
	return ""
}
