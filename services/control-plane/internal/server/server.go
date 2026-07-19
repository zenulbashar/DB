// Package server wires the Phase 1 REST surface (api/openapi.yaml) onto the
// store. Handlers stay thin: decode → authorize (scope + org match) → store →
// audit → encode.
package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/secrets"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

type Config struct {
	// BootstrapToken enables POST /v1/bootstrap while the platform has no org
	// (ADR-013). Empty disables the endpoint entirely.
	BootstrapToken string
	// GatewayToken authenticates the pg-gateway's privileged wake call
	// (POST /internal/branches/{br}/wake; ADR-014). Empty disables the entire
	// /internal surface (SECURITY_MODEL §3).
	GatewayToken string
	Version      string
	// Keyring encrypts tenant secret material (SECURITY_MODEL §5). Required.
	Keyring *secrets.Keyring
}

type Server struct {
	store    store.Store
	cfg      Config
	log      *slog.Logger
	mux      *chi.Mux
	idemKeys *keyedMutex
}

func New(st store.Store, cfg Config, log *slog.Logger) *Server {
	s := &Server{store: st, cfg: cfg, log: log, mux: chi.NewRouter(), idemKeys: newKeyedMutex()}
	s.routes()
	return s
}

// newPassword is indirected for tests that need deterministic credentials.
func (s *Server) newPassword() (string, error) { return secrets.NewPassword() }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	s.mux.Use(s.requestID, s.logging, s.recoverer)

	// /healthz for k8s probes; /v1/healthz so the documented API base
	// (which includes /v1) resolves for generated clients.
	s.mux.Get("/healthz", s.handleHealth)
	s.mux.Get("/v1/healthz", s.handleHealth)
	s.mux.Post("/v1/bootstrap", s.handleBootstrap)

	// Internal platform surface: not part of the tenant API, authenticated by
	// the shared gateway token (disabled entirely when unset). ADR-014.
	s.mux.Route("/internal", func(r chi.Router) {
		r.Post("/branches/{br}/wake", s.authenticateGateway(s.handleWakeBranchInternal))
		r.Post("/gateway-activity", s.authenticateGateway(s.handleGatewayActivity))
	})

	s.mux.Route("/v1", func(r chi.Router) {
		r.Use(s.authenticate)

		r.Get("/orgs", s.requireScope(domain.ScopeOrgsRead, s.handleListOrgs))
		r.Route("/orgs/{org}", func(r chi.Router) {
			r.Get("/", s.requireScope(domain.ScopeOrgsRead, s.handleGetOrg))
			r.Patch("/", s.requireScope(domain.ScopeOrgsWrite, s.handleUpdateOrg))

			r.Get("/members", s.requireScope(domain.ScopeOrgsRead, s.handleListMembers))
			r.Post("/members", s.requireScope(domain.ScopeMembersManage, s.idempotent(s.handleAddMember)))
			r.Patch("/members/{user}", s.requireScope(domain.ScopeMembersManage, s.handleUpdateMemberRole))
			r.Delete("/members/{user}", s.requireScope(domain.ScopeMembersManage, s.handleRemoveMember))

			r.Get("/api-keys", s.requireScope(domain.ScopeKeysManage, s.handleListAPIKeys))
			r.Post("/api-keys", s.requireScope(domain.ScopeKeysManage, s.idempotent(s.handleCreateAPIKey)))
			r.Delete("/api-keys/{key}", s.requireScope(domain.ScopeKeysManage, s.handleRevokeAPIKey))

			r.Get("/audit-log", s.requireScope(domain.ScopeAuditRead, s.handleListAudit))
		})

		r.Get("/projects", s.requireScope(domain.ScopeProjectsRead, s.handleListProjects))
		r.Post("/projects", s.requireScope(domain.ScopeProjectsWrite, s.idempotent(s.handleCreateProject)))
		r.Route("/projects/{prj}", func(r chi.Router) {
			r.Get("/", s.requireScope(domain.ScopeProjectsRead, s.handleGetProject))
			r.Patch("/", s.requireScope(domain.ScopeProjectsWrite, s.handleUpdateProject))
			r.Delete("/", s.requireScope(domain.ScopeProjectsWrite, s.handleDeleteProject))
			r.Get("/branches", s.requireScope(domain.ScopeBranchesRead, s.handleListBranches))
			r.Post("/branches", s.requireScope(domain.ScopeBranchesWrite, s.idempotent(s.handleCreateBranch)))
			r.Get("/connection-uri", s.requireScope(domain.ScopeRolesRead, s.handleConnectionURI))
			r.Get("/imports", s.requireScope(domain.ScopeImportsRead, s.handleListImports))
			r.Post("/imports", s.requireScope(domain.ScopeImportsWrite, s.idempotent(s.handleCreateImport)))
		})
		r.Route("/imports/{imp}", func(r chi.Router) {
			r.Get("/", s.requireScope(domain.ScopeImportsRead, s.handleGetImport))
			r.Post("/cutover", s.requireScope(domain.ScopeImportsWrite, s.handleImportCutover))
			r.Post("/abort", s.requireScope(domain.ScopeImportsWrite, s.handleImportAbort))
			r.Patch("/state", s.requireScope(domain.ScopeImportsWrite, s.handleImportState))
		})
		r.Route("/branches/{br}", func(r chi.Router) {
			r.Get("/", s.requireScope(domain.ScopeBranchesRead, s.handleGetBranch))
			r.Patch("/", s.requireScope(domain.ScopeBranchesWrite, s.handleUpdateBranch))
			r.Delete("/", s.requireScope(domain.ScopeBranchesWrite, s.handleDeleteBranch))
			r.Post("/suspend", s.requireScope(domain.ScopeBranchesWrite, s.handleSuspendBranch))
			r.Post("/resume", s.requireScope(domain.ScopeBranchesWrite, s.handleResumeBranch))
			r.Get("/endpoints", s.requireScope(domain.ScopeEndpointsRead, s.handleListEndpoints))

			r.Get("/roles", s.requireScope(domain.ScopeRolesRead, s.handleListDBRoles))
			r.Post("/roles", s.requireScope(domain.ScopeRolesWrite, s.idempotent(s.handleCreateDBRole)))
			r.Delete("/roles/{role}", s.requireScope(domain.ScopeRolesWrite, s.handleDeleteDBRole))
			r.Post("/roles/{role}/reset-password", s.requireScope(domain.ScopeRolesWrite, s.handleResetDBRolePassword))

			r.Get("/databases", s.requireScope(domain.ScopeRolesRead, s.handleListDatabases))
			r.Post("/databases", s.requireScope(domain.ScopeRolesWrite, s.idempotent(s.handleCreateDatabase)))
			r.Delete("/databases/{db}", s.requireScope(domain.ScopeRolesWrite, s.handleDeleteDatabase))
		})
	})
}

// orgFromPath authorizes the {org} path parameter against the principal's org.
// Cross-org IDs return 404 (not 403) to avoid resource enumeration
// (MULTI_TENANCY §7).
func (s *Server) orgFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := chi.URLParam(r, "org")
	p := principalFrom(r.Context())
	if p == nil || p.OrgID != orgID {
		writeProblem(w, r, http.StatusNotFound, "not-found", "Not found", "")
		return "", false
	}
	return orgID, true
}
