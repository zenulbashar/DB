package server

import (
	"net/http"
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
		Role            domain.BranchRole `json:"role"`
		ComputeMinCU    *float64          `json:"compute_min_cu"`
		ComputeMaxCU    *float64          `json:"compute_max_cu"`
		SuspendTimeoutS *int              `json:"suspend_timeout_s"`
	}
	if !decode(w, r, &req) {
		return
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
