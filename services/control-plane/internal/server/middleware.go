package server

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
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

// authenticate resolves the Bearer zdb_ token to a Principal. Routes mounted
// behind it always have a principal; scope checks happen per handler.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || !auth.WellFormed(strings.TrimSpace(token)) {
			writeProblem(w, r, http.StatusUnauthorized, "unauthenticated",
				"Authentication required", "Provide an API key: Authorization: Bearer zdb_…")
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
// idempotent replays stored responses for repeated Idempotency-Key POSTs
// (API_SPECIFICATION §1). Two properties matter for a credential-issuing API:
//   - Concurrency: same-key requests are serialized per instance (keyed lock)
//     so two racing POSTs cannot both execute and create duplicate resources
//     (audit finding: idempotency TOCTOU).
//   - At-rest secrecy: create responses carry one-time secrets (API tokens,
//     DB passwords). The cached copy is envelope-encrypted with the same
//     keyring that protects role secrets, so no plaintext credential persists
//     in idempotency_keys (audit finding: credential-at-rest). The client
//     still receives plaintext on the live call and on replay.
func (s *Server) idempotent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		p := principalFrom(r.Context())
		if key == "" || p == nil {
			h(w, r)
			return
		}
		route := r.Method + " " + r.URL.Path
		unlock := s.idemKeys.lock(p.OrgID + "\x00" + route + "\x00" + key)
		defer unlock()

		if prev, err := s.store.GetIdempotent(r.Context(), p.OrgID, route, key); err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotency-Replayed", "true")
			w.WriteHeader(prev.Status)
			_, _ = w.Write(s.decodeIdem(prev.Body))
			return
		}
		rec := &recordingWriter{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		if rec.status >= 200 && rec.status < 300 {
			_ = s.store.PutIdempotent(r.Context(), p.OrgID, route, key,
				store.IdempotentResponse{Status: rec.status, Body: s.encodeIdem(rec.buf.Bytes())},
				time.Now().Add(24*time.Hour))
		}
	}
}

// encodeIdem/decodeIdem envelope-encrypt the cached body when a keyring is
// configured. A leading 0x01 marks encrypted blobs so mixed-vintage rows read
// correctly across a rollout.
func (s *Server) encodeIdem(body []byte) []byte {
	if s.cfg.Keyring == nil {
		return append([]byte{0x00}, body...)
	}
	ct, _, err := s.cfg.Keyring.Encrypt(body)
	if err != nil {
		s.log.Error("idempotency encrypt failed; not caching", "err", err)
		return append([]byte{0x00}, body...)
	}
	return append([]byte{0x01}, ct...)
}

func (s *Server) decodeIdem(stored []byte) []byte {
	if len(stored) == 0 {
		return stored
	}
	body := stored[1:]
	if stored[0] == 0x01 && s.cfg.Keyring != nil {
		if pt, err := s.cfg.Keyring.Decrypt(body); err == nil {
			return pt
		}
	}
	return body
}

// keyedMutex serializes work per string key with reference-counted cleanup so
// the map never grows without bound.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*lockEntry
}

type lockEntry struct {
	mu   sync.Mutex
	refs int
}

func newKeyedMutex() *keyedMutex { return &keyedMutex{locks: map[string]*lockEntry{}} }

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	e := k.locks[key]
	if e == nil {
		e = &lockEntry{}
		k.locks[key] = e
	}
	e.refs++
	k.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
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
