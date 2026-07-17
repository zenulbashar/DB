package server

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/zenulbashar/DB/services/control-plane/internal/auth"
	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/ids"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxPrincipal
)

// Principal is the authenticated caller: an org-scoped API key.
type Principal struct {
	OrgID  string
	KeyID  string
	Scopes []domain.Scope
}

func (p *Principal) Has(s domain.Scope) bool {
	for _, sc := range p.Scopes {
		if sc == s {
			return true
		}
	}
	return false
}

func requestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxRequestID).(string)
	return v
}

func principalFrom(ctx context.Context) *Principal {
	v, _ := ctx.Value(ctxPrincipal).(*Principal)
	return v
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ids.New(ids.Request)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "http",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", sw.status),
			slog.Duration("dur", time.Since(start)),
			slog.String("request_id", requestIDFrom(r.Context())),
		)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "err", rec, "path", r.URL.Path, "request_id", requestIDFrom(r.Context()))
				writeProblem(w, r, http.StatusInternalServerError, "internal", "Internal server error", "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// authenticate resolves the Bearer ndb_ token to a Principal. Routes mounted
// behind it always have a principal; scope checks happen per handler.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || !auth.WellFormed(strings.TrimSpace(token)) {
			writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
				"Authentication required", "Provide an API key: Authorization: Bearer ndb_…")
			return
		}
		key, err := s.store.FindAPIKeyByHash(r.Context(), auth.HashToken(strings.TrimSpace(token)))
		if err != nil {
			writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
				"Invalid API key", "The key is unknown, revoked, or expired.")
			return
		}
		// last_used tracking is best-effort; never block the request on it.
		go func(id string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.store.TouchAPIKey(ctx, id, time.Now().UTC())
		}(key.Key.ID)

		p := &Principal{OrgID: key.Key.OrgID, KeyID: key.Key.ID, Scopes: key.Key.Scopes}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxPrincipal, p)))
	})
}

// requireScope guards a handler with a scope check.
func (s *Server) requireScope(scope domain.Scope, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r.Context())
		if p == nil || !p.Has(scope) {
			writeProblem(w, r, http.StatusForbidden, "insufficient-scope",
				"Insufficient scope", "This operation requires the '"+string(scope)+"' scope.")
			return
		}
		h(w, r)
	}
}

// idempotent replays stored responses for repeated Idempotency-Key POSTs
// (API_SPECIFICATION §1). Only successful (2xx) responses are stored.
func (s *Server) idempotent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		p := principalFrom(r.Context())
		if key == "" || p == nil {
			h(w, r)
			return
		}
		route := r.Method + " " + r.URL.Path
		if prev, err := s.store.GetIdempotent(r.Context(), p.OrgID, route, key); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotency-Replayed", "true")
			w.WriteHeader(prev.Status)
			_, _ = w.Write(prev.Body)
			return
		}
		rec := &recordingWriter{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		if rec.status >= 200 && rec.status < 300 {
			_ = s.store.PutIdempotent(r.Context(), p.OrgID, route, key,
				store.IdempotentResponse{Status: rec.status, Body: rec.buf.Bytes()},
				time.Now().Add(24*time.Hour))
		}
	}
}

func clientIP(r *http.Request) *string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "" {
		return nil
	}
	return &host
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

type recordingWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (w *recordingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}
