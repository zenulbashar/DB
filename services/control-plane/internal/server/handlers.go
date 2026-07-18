package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/auth"
	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/ids"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

// --- system ---

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.cfg.Version})
}

type bootstrapRequest struct {
	BootstrapToken string  `json:"bootstrap_token"`
	Email          string  `json:"email"`
	Name           *string `json:"name"`
	OrgName        string  `json:"org_name"`
}

type apiKeyWithSecret struct {
	domain.APIKey
	Token string `json:"token"`
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	var req bootstrapRequest
	if !decode(w, r, &req) {
		return
	}
	if s.cfg.BootstrapToken == "" ||
		subtle.ConstantTimeCompare([]byte(req.BootstrapToken), []byte(s.cfg.BootstrapToken)) != 1 {
		writeProblem(w, r, http.StatusForbidden, "bootstrap-forbidden",
			"Bootstrap not permitted", "Bootstrap is disabled or the token is wrong.")
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil || req.OrgName == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation",
			"Validation failed", "email must be valid and org_name is required.")
		return
	}
	token, hash, prefix := auth.NewToken()
	res, err := s.store.Bootstrap(r.Context(), store.BootstrapParams{
		Email: req.Email, Name: req.Name, OrgName: req.OrgName,
		KeyName: "bootstrap-owner", KeyHash: hash, KeyPrefix: prefix,
		Scopes: domain.AllScopes,
	})
	if errors.Is(err, store.ErrAlreadyBoot) {
		writeProblem(w, r, http.StatusConflict, "already-bootstrapped",
			"Platform already bootstrapped", "An organization already exists.")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, res.Org.ID, domain.ActorSystem, "bootstrap", "platform.bootstrap", "org", res.Org.ID, nil)
	writeJSON(w, http.StatusCreated, map[string]any{
		"org":     res.Org,
		"user":    res.User,
		"api_key": apiKeyWithSecret{APIKey: res.APIKey, Token: token},
	})
}

// --- orgs ---

func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	org, err := s.store.GetOrg(r.Context(), p.OrgID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": []domain.Org{*org}, "next_cursor": nil})
}

func (s *Server) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgFromPath(w, r)
	if !ok {
		return
	}
	org, err := s.store.GetOrg(r.Context(), orgID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, org)
}

func (s *Server) handleUpdateOrg(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgFromPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" || len(req.Name) > 120 {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "name must be 1–120 characters.")
		return
	}
	org, err := s.store.UpdateOrgName(r.Context(), orgID, req.Name)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, orgID, "org.update", "org", orgID, map[string]any{"name": req.Name})
	writeJSON(w, http.StatusOK, org)
}

// --- members ---

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgFromPath(w, r)
	if !ok {
		return
	}
	members, err := s.store.ListMembers(r.Context(), orgID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": emptyIfNil(members)})
}

func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgFromPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Email string         `json:"email"`
		Name  *string        `json:"name"`
		Role  domain.OrgRole `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil || !req.Role.Valid() {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "email must be valid; role must be owner|admin|member|viewer.")
		return
	}
	m, err := s.store.AddMember(r.Context(), orgID, req.Email, req.Name, req.Role)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, orgID, "member.add", "user", m.User.ID, map[string]any{"role": m.Role})
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) handleUpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgFromPath(w, r)
	if !ok {
		return
	}
	userID := chi.URLParam(r, "user")
	var req struct {
		Role domain.OrgRole `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !req.Role.Valid() {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "role must be owner|admin|member|viewer.")
		return
	}
	m, err := s.store.UpdateMemberRole(r.Context(), orgID, userID, req.Role)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, orgID, "member.update_role", "user", userID, map[string]any{"role": req.Role})
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgFromPath(w, r)
	if !ok {
		return
	}
	userID := chi.URLParam(r, "user")
	if err := s.store.RemoveMember(r.Context(), orgID, userID); err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, orgID, "member.remove", "user", userID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// --- api keys ---

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgFromPath(w, r)
	if !ok {
		return
	}
	keys, err := s.store.ListAPIKeys(r.Context(), orgID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": emptyIfNil(keys)})
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgFromPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Name      string         `json:"name"`
		Scopes    []domain.Scope `json:"scopes"`
		ExpiresAt *time.Time     `json:"expires_at"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" || len(req.Name) > 120 || len(req.Scopes) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "name (1–120 chars) and at least one scope are required.")
		return
	}
	for _, sc := range req.Scopes {
		if !domain.ValidScope(sc) {
			writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "unknown scope: "+string(sc))
			return
		}
	}
	token, hash, prefix := auth.NewToken()
	key, err := s.store.CreateAPIKey(r.Context(), store.CreateAPIKeyParams{
		OrgID: orgID, Name: req.Name, Hash: hash, Prefix: prefix,
		Scopes: req.Scopes, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, orgID, "api_key.create", "api_key", key.ID, map[string]any{"scopes": req.Scopes})
	writeJSON(w, http.StatusCreated, apiKeyWithSecret{APIKey: *key, Token: token})
}

func (s *Server) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgFromPath(w, r)
	if !ok {
		return
	}
	keyID := chi.URLParam(r, "key")
	if err := s.store.RevokeAPIKey(r.Context(), orgID, keyID, time.Now().UTC()); err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, orgID, "api_key.revoke", "api_key", keyID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// --- audit ---

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	orgID, ok := s.orgFromPath(w, r)
	if !ok {
		return
	}
	entries, next, err := s.store.ListAudit(r.Context(), orgID, pageFrom(r))
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": emptyIfNil(entries), "next_cursor": nullable(next)})
}

// --- projects ---

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	// ?org= is accepted for forward compatibility but must match the key's org.
	if q := r.URL.Query().Get("org"); q != "" && q != p.OrgID {
		writeProblem(w, r, http.StatusNotFound, "not-found", "Not found", "")
		return
	}
	projects, next, err := s.store.ListProjects(r.Context(), p.OrgID, pageFrom(r))
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": emptyIfNil(projects), "next_cursor": nullable(next)})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var req struct {
		OrgID     string `json:"org_id"`
		Name      string `json:"name"`
		Region    string `json:"region"`
		PGVersion int    `json:"pg_version"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.OrgID == "" {
		req.OrgID = p.OrgID
	}
	if req.OrgID != p.OrgID {
		writeProblem(w, r, http.StatusNotFound, "not-found", "Not found", "")
		return
	}
	if req.Name == "" || len(req.Name) > 120 {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "name must be 1–120 characters.")
		return
	}
	if req.Region == "" {
		req.Region = "syd1"
	}
	if req.Region != "syd1" {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "unknown region: "+req.Region)
		return
	}
	if req.PGVersion == 0 {
		req.PGVersion = 17
	}
	if req.PGVersion != 16 && req.PGVersion != 17 {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "pg_version must be 16 or 17.")
		return
	}
	prj, err := s.store.CreateProject(r.Context(), store.CreateProjectParams{
		OrgID: req.OrgID, Name: req.Name, Region: req.Region, PGVersion: req.PGVersion,
	})
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, req.OrgID, "project.create", "project", prj.ID,
		map[string]any{"region": prj.Region, "pg_version": prj.PGVersion})
	writeJSON(w, http.StatusCreated, prj)
}

func (s *Server) projectFromPath(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	p := principalFrom(r.Context())
	return p.OrgID, chi.URLParam(r, "prj"), true
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	orgID, prjID, _ := s.projectFromPath(w, r)
	prj, err := s.store.GetProject(r.Context(), orgID, prjID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, prj)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	orgID, prjID, _ := s.projectFromPath(w, r)
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" || len(req.Name) > 120 {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "name must be 1–120 characters.")
		return
	}
	prj, err := s.store.UpdateProjectName(r.Context(), orgID, prjID, req.Name)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, orgID, "project.update", "project", prjID, map[string]any{"name": req.Name})
	writeJSON(w, http.StatusOK, prj)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	orgID, prjID, _ := s.projectFromPath(w, r)
	if err := s.store.SoftDeleteProject(r.Context(), orgID, prjID, time.Now().UTC()); err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, orgID, "project.delete", "project", prjID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// --- shared helpers ---

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "malformed-body", "Malformed request body", err.Error())
		return false
	}
	return true
}

func pageFrom(r *http.Request) store.Page {
	q := r.URL.Query()
	limit := 0
	if l := q.Get("limit"); l != "" {
		_, _ = jsonNumber(l, &limit)
	}
	return store.Page{Limit: limit, Cursor: q.Get("cursor")}
}

func jsonNumber(s string, out *int) (bool, error) {
	var n int
	if err := json.Unmarshal([]byte(s), &n); err != nil {
		return false, err
	}
	*out = n
	return true, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func emptyIfNil[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

// audit writes an audit entry; failures are logged, never user-facing —
// except that mutations still succeeded, which is the accepted Phase 1
// trade-off (Phase 2 moves audit writes into the mutation transaction).
func (s *Server) audit(r *http.Request, orgID string, actorType domain.ActorType, actorID, action, targetType, targetID string, details map[string]any) {
	e := domain.AuditEntry{
		ID: ids.New(ids.Audit), OrgID: orgID, ActorType: actorType, ActorID: actorID,
		Action: action, TargetType: targetType, TargetID: targetID,
		IP: clientIP(r), At: time.Now().UTC(), Details: details,
	}
	if err := s.store.AppendAudit(r.Context(), e); err != nil {
		s.log.Error("audit write failed", "err", err, "action", action, "org", orgID)
	}
}

func (s *Server) auditKey(r *http.Request, orgID, action, targetType, targetID string, details map[string]any) {
	p := principalFrom(r.Context())
	actorID := "unknown"
	if p != nil {
		actorID = p.KeyID
	}
	s.audit(r, orgID, domain.ActorAPIKey, actorID, action, targetType, targetID, details)
}

func (s *Server) storeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not-found", "Not found", "")
	case errors.Is(err, store.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", "Conflict", "The resource already exists or conflicts with another.")
	case errors.Is(err, store.ErrLastOwner):
		writeProblem(w, r, http.StatusConflict, "last-owner", "Would remove the last owner", "Assign another owner first.")
	case errors.Is(err, store.ErrDefaultBranch):
		writeProblem(w, r, http.StatusConflict, "default-branch", "Cannot delete the default branch", "Delete the project instead, or change the default branch first.")
	default:
		s.internal(w, r, err)
	}
}

func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("internal error", "err", err, "path", r.URL.Path, "request_id", requestIDFrom(r.Context()))
	writeProblem(w, r, http.StatusInternalServerError, "internal", "Internal server error", "")
}
