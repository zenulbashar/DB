package server

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

// Postgres identifier discipline for tenant-chosen names: lowercase snake,
// unquoted-identifier-safe, and never colliding with platform-owned roles.
var pgNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

var reservedPGNames = map[string]bool{
	"postgres": true, "template0": true, "template1": true,
	"streaming_replica": true, "cnpg_pooler_pgbouncer": true, "pgbouncer": true,
}

func validPGName(name string) bool {
	return pgNameRE.MatchString(name) && !reservedPGNames[name] && !strings.HasPrefix(name, "pg_")
}

type roleWithPassword struct {
	domain.DBRole
	Password string `json:"password"`
}

func (s *Server) sealPassword(w http.ResponseWriter, r *http.Request) (password string, material store.SecretMaterial, ok bool) {
	password, err := s.newPassword()
	if err != nil {
		s.internal(w, r, err)
		return "", store.SecretMaterial{}, false
	}
	ciphertext, version, err := s.cfg.Keyring.Encrypt([]byte(password))
	if err != nil {
		s.internal(w, r, err)
		return "", store.SecretMaterial{}, false
	}
	return password, store.SecretMaterial{Ciphertext: ciphertext, KeyVersion: version}, true
}

func (s *Server) handleListDBRoles(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	roles, err := s.store.ListDBRoles(r.Context(), p.OrgID, chi.URLParam(r, "br"))
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": emptyIfNil(roles)})
}

func (s *Server) handleCreateDBRole(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brID := chi.URLParam(r, "br")
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validPGName(req.Name) {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed",
			"name must match ^[a-z_][a-z0-9_]{0,62}$ and not be reserved.")
		return
	}
	password, material, ok := s.sealPassword(w, r)
	if !ok {
		return
	}
	role, err := s.store.CreateDBRole(r.Context(), store.CreateDBRoleParams{
		OrgID: p.OrgID, BranchID: brID, Name: req.Name, Secret: material,
	})
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "db_role.create", "db_role", role.ID, map[string]any{"branch": brID, "name": role.Name})
	writeJSON(w, http.StatusCreated, roleWithPassword{DBRole: *role, Password: password})
}

func (s *Server) handleDeleteDBRole(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brID, name := chi.URLParam(r, "br"), chi.URLParam(r, "role")
	if err := s.store.DeleteDBRole(r.Context(), p.OrgID, brID, name); err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "db_role.delete", "db_role", name, map[string]any{"branch": brID})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResetDBRolePassword(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brID, name := chi.URLParam(r, "br"), chi.URLParam(r, "role")
	password, material, ok := s.sealPassword(w, r)
	if !ok {
		return
	}
	if err := s.store.ResetDBRolePassword(r.Context(), p.OrgID, brID, name, material); err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "db_role.reset_password", "db_role", name, map[string]any{"branch": brID})
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "password": password})
}

func (s *Server) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	dbs, err := s.store.ListDatabases(r.Context(), p.OrgID, chi.URLParam(r, "br"))
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": emptyIfNil(dbs)})
}

func (s *Server) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brID := chi.URLParam(r, "br")
	var req struct {
		Name      string `json:"name"`
		OwnerRole string `json:"owner_role"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validPGName(req.Name) || req.OwnerRole == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed",
			"name must be a valid identifier and owner_role is required.")
		return
	}
	db, err := s.store.CreateDatabase(r.Context(), p.OrgID, brID, req.Name, req.OwnerRole)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "database.create", "database", db.ID, map[string]any{"branch": brID, "name": db.Name})
	writeJSON(w, http.StatusCreated, db)
}

func (s *Server) handleDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	brID, name := chi.URLParam(r, "br"), chi.URLParam(r, "db")
	if err := s.store.DeleteDatabase(r.Context(), p.OrgID, brID, name); err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "database.delete", "database", name, map[string]any{"branch": brID})
	w.WriteHeader(http.StatusNoContent)
}

// handleConnectionURI assembles a connection string server-side
// (API_SPECIFICATION §5): masked by default; ?reveal=true requires
// roles:write and is always audited.
func (s *Server) handleConnectionURI(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	prjID := chi.URLParam(r, "prj")
	q := r.URL.Query()

	prj, err := s.store.GetProject(r.Context(), p.OrgID, prjID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	branchID := q.Get("branch")
	if branchID == "" {
		if prj.DefaultBranchID == nil {
			writeProblem(w, r, http.StatusConflict, "no-default-branch", "Project has no default branch", "")
			return
		}
		branchID = *prj.DefaultBranchID
	}
	branch, err := s.store.GetBranch(r.Context(), p.OrgID, branchID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	if branch.ProjectID != prjID {
		writeProblem(w, r, http.StatusNotFound, "not-found", "Not found", "")
		return
	}

	kind := domain.EndpointKind(q.Get("endpoint"))
	if kind == "" {
		kind = domain.EndpointRWPooled
	}
	var host string
	for _, ep := range branch.Endpoints {
		if ep.Kind == kind {
			host = ep.Host
		}
	}
	if host == "" {
		writeProblem(w, r, http.StatusNotFound, "not-found", "Endpoint kind not available on this branch", "")
		return
	}

	roles, err := s.store.ListDBRoles(r.Context(), p.OrgID, branchID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	dbs, err := s.store.ListDatabases(r.Context(), p.OrgID, branchID)
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	if len(roles) == 0 || len(dbs) == 0 {
		writeProblem(w, r, http.StatusConflict, "no-credentials", "Branch has no roles or databases yet", "")
		return
	}
	roleName := q.Get("role")
	if roleName == "" {
		roleName = roles[0].Name
	}
	dbName := q.Get("database")
	if dbName == "" {
		dbName = dbs[0].Name
	}

	password := "****"
	if q.Get("reveal") == "true" {
		if !p.Has(domain.ScopeRolesWrite) {
			writeProblem(w, r, http.StatusForbidden, "insufficient-scope",
				"Insufficient scope", "Revealing credentials requires the 'roles:write' scope.")
			return
		}
		ciphertext, err := s.store.GetDBRoleSecret(r.Context(), p.OrgID, branchID, roleName)
		if err != nil {
			s.storeErr(w, r, err)
			return
		}
		plain, err := s.cfg.Keyring.Decrypt(ciphertext)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		password = string(plain)
		s.auditKey(r, p.OrgID, "connection_uri.reveal", "db_role", roleName,
			map[string]any{"branch": branchID, "endpoint": string(kind)})
	}

	uri := fmt.Sprintf("postgresql://%s:%s@%s/%s?sslmode=require", roleName, password, host, dbName)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"uri": uri, "endpoint": string(kind), "branch_id": branchID,
		"role": roleName, "database": dbName, "revealed": password != "****",
	})
}
