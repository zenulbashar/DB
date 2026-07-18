package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

func (s *Server) handleListImports(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	imports, next, err := s.store.ListImports(r.Context(), p.OrgID, chi.URLParam(r, "prj"), pageFrom(r))
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": emptyIfNil(imports), "next_cursor": nullable(next)})
}

func (s *Server) handleCreateImport(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	prjID := chi.URLParam(r, "prj")
	var req struct {
		SourceKind   domain.ImportSourceKind `json:"source_kind"`
		Mode         domain.ImportMode       `json:"mode"`
		SourceURL    string                  `json:"source_url"`
		TargetBranch *string                 `json:"target_branch"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !req.SourceKind.Valid() || !req.Mode.Valid() {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed",
			"source_kind must be neon|supabase|rds|azure|generic; mode must be dump_restore|logical_replication.")
		return
	}
	u, err := url.Parse(req.SourceURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed",
			"source_url must be a postgres:// connection URL.")
		return
	}
	// The source URL is a credential: envelope-encrypt it immediately and
	// never echo it back (SECURITY_MODEL §5).
	ciphertext, version, err := s.cfg.Keyring.Encrypt([]byte(req.SourceURL))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	im, err := s.store.CreateImport(r.Context(), store.CreateImportParams{
		OrgID: p.OrgID, ProjectID: prjID, TargetBranchID: req.TargetBranch,
		SourceKind: req.SourceKind, Mode: req.Mode,
		SourceSecret: store.SecretMaterial{Ciphertext: ciphertext, KeyVersion: version},
	})
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "import.create", "import", im.ID, map[string]any{
		"project": prjID, "source_kind": im.SourceKind, "mode": im.Mode,
		// Host only — never the credential-bearing URL.
		"source_host": u.Host,
	})
	writeJSON(w, http.StatusCreated, im)
}

func (s *Server) handleGetImport(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	im, err := s.store.GetImport(r.Context(), p.OrgID, chi.URLParam(r, "imp"))
	if err != nil {
		s.storeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, im)
}

// handleImportCutover is the human gate (MIGRATION_STRATEGY §2 stage 5):
// only a cutover_ready import may proceed.
func (s *Server) handleImportCutover(w http.ResponseWriter, r *http.Request) {
	s.transitionImport(w, r, domain.ImportCutOver, "import.cutover", nil)
}

func (s *Server) handleImportAbort(w http.ResponseWriter, r *http.Request) {
	msg := "aborted by operator"
	s.transitionImport(w, r, domain.ImportAborted, "import.abort", &msg)
}

// handleImportState is the runner-facing transition endpoint (imports:write);
// the state machine in the store rejects illegal steps.
func (s *Server) handleImportState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State       domain.ImportState `json:"state"`
		Report      map[string]any     `json:"report"`
		Checkpoints map[string]any     `json:"checkpoints"`
		Error       *string            `json:"error"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.State == "" {
		writeProblem(w, r, http.StatusBadRequest, "validation", "Validation failed", "state is required.")
		return
	}
	p := principalFrom(r.Context())
	im, err := s.store.TransitionImport(r.Context(), p.OrgID, chi.URLParam(r, "imp"),
		store.TransitionImportParams{To: req.State, Report: req.Report, Checkpoints: req.Checkpoints, Error: req.Error})
	if err != nil {
		s.importErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, "import.transition", "import", im.ID, map[string]any{"state": im.State})
	writeJSON(w, http.StatusOK, im)
}

func (s *Server) transitionImport(w http.ResponseWriter, r *http.Request, to domain.ImportState, action string, errMsg *string) {
	p := principalFrom(r.Context())
	impID := chi.URLParam(r, "imp")
	im, err := s.store.TransitionImport(r.Context(), p.OrgID, impID,
		store.TransitionImportParams{To: to, Error: errMsg})
	if err != nil {
		s.importErr(w, r, err)
		return
	}
	s.auditKey(r, p.OrgID, action, "import", impID, map[string]any{"state": im.State})
	writeJSON(w, http.StatusOK, im)
}

func (s *Server) importErr(w http.ResponseWriter, r *http.Request, err error) {
	if strings.Contains(err.Error(), "conflict") || err == store.ErrConflict {
		writeProblem(w, r, http.StatusConflict, "illegal-transition",
			"Illegal import state transition",
			"The import is not in a state that allows this action.")
		return
	}
	s.storeErr(w, r, err)
}
