package server

import (
	"encoding/json"
	"net/http"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
)

// problem writes an RFC 9457 application/problem+json response
// (API_SPECIFICATION §1 error model).
type problemBody struct {
	Type      string `json:"type,omitempty"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// problemTypeBase derives the RFC 9457 type-URI prefix from the configured
// platform domain (NDB_DOMAIN, ADR-020).
func problemTypeBase() string { return "https://api." + domain.BaseDomain() + "/errors/" }

func writeProblem(w http.ResponseWriter, r *http.Request, status int, slug, title, detail string) {
	body := problemBody{
		Title:     title,
		Status:    status,
		Detail:    detail,
		RequestID: requestIDFrom(r.Context()),
	}
	if slug != "" {
		body.Type = problemTypeBase() + slug
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
