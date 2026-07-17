// Package server wires the Phase 1 REST surface (api/openapi.yaml) onto the
// store. Handlers stay thin: decode → authorize (scope + org match) → store →
// audit → encode.
package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

type Config struct {
	// BootstrapToken enables POST /v1/bootstrap while the platform has no org
	// (ADR-013). Empty disables the endpoint entirely.
	BootstrapToken string
	Version        string
}

type Server struct {
	store store.Store
	cfg   Config
	log   *slog.Logger
	mux   *chi.Mux
}

func New(st store.Store, cfg Config, log *slog.Logger) *Server {
	s := &Server{store: st, cfg: cfg, log: log, mux: chi.NewRouter()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	s.mux.Use(s.requestID, s.logging, s.recoverer)

	s.mux.Get("/healthz", s.handleHealth)
	s.mux.Post("/v1/bootstrap", s.handleBootstrap)

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
