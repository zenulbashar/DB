// Package proxy implements the pg-gateway data path (ADR-007): terminate
// client TLS, route by SNI (or the options fallback) to the branch's backend,
// then become a transparent byte pipe. Wake-on-connect holding arrives in
// Phase 4; v1 rejects suspended endpoints with a clear error.
package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/zenulbashar/DB/services/pg-gateway/internal/routes"
)

type Config struct {
	TLS *tls.Config
	// AllowInsecure permits non-TLS clients (tests / trusted in-cluster hops).
	// Production keeps this false: TLS-only endpoints (SECURITY_MODEL §5).
	AllowInsecure bool
	// DialTimeout bounds the backend dial.
	DialTimeout time.Duration
}

type Metrics struct {
	ActiveConns   *prometheus.GaugeVec
	ConnsTotal    *prometheus.CounterVec
	Bytes         *prometheus.CounterVec
	ConnectErrors *prometheus.CounterVec
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		ActiveConns: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "pggw_active_connections", Help: "Active proxied connections per endpoint.",
		}, []string{"endpoint"}),
		ConnsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pggw_connections_total", Help: "Accepted connections per endpoint.",
		}, []string{"endpoint"}),
		Bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pggw_bytes_total", Help: "Bytes proxied per endpoint and direction.",
		}, []string{"endpoint", "direction"}),
		ConnectErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pggw_connect_errors_total", Help: "Connection-phase failures by reason.",
		}, []string{"reason"}),
	}
	reg.MustRegister(m.ActiveConns, m.ConnsTotal, m.Bytes, m.ConnectErrors)
	return m
}

type Server struct {
	cfg    Config
	table  *routes.Table
	log    *slog.Logger
	m      *Metrics
	countM sync.Mutex
	counts map[string]int
}

func New(cfg Config, table *routes.Table, m *Metrics, log *slog.Logger) *Server {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	return &Server{cfg: cfg, table: table, log: log, m: m, counts: map[string]int{}}
}

// Serve accepts connections until the listener closes or ctx is done.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(ctx context.Context, raw net.Conn) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(30 * time.Second)) // startup phase only

	init, err := ReadInitial(raw)
	if err != nil {
		s.fail(raw, "read-initial", "08P01", "invalid startup packet")
		return
	}
	if init.Kind == KindGSSENC {
		// GSSAPI encryption unsupported; libpq falls back to SSLRequest.
		if _, err := raw.Write([]byte{'N'}); err != nil {
			return
		}
		if init, err = ReadInitial(raw); err != nil {
			s.fail(raw, "read-initial", "08P01", "invalid startup packet")
			return
		}
	}

	var client net.Conn = raw
	sni := ""
	switch init.Kind {
	case KindSSLRequest:
		if _, err := raw.Write([]byte{'S'}); err != nil {
			return
		}
		tlsConn := tls.Server(raw, s.cfg.TLS)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			s.m.ConnectErrors.WithLabelValues("tls-handshake").Inc()
			return
		}
		sni = tlsConn.ConnectionState().ServerName
		client = tlsConn
		if init, err = ReadInitial(client); err != nil {
			s.fail(client, "read-startup", "08P01", "invalid startup packet")
			return
		}
	case KindStartup, KindCancel:
		if !s.cfg.AllowInsecure {
			s.fail(client, "plaintext-rejected", "08P01",
				"SSL required: connect with sslmode=require")
			return
		}
	}

	endpointID := EndpointFromSNI(sni)
	forward := init.Raw
	if endpointID == "" && init.Params != nil {
		if endpointID = EndpointFromOptions(init.Params); endpointID != "" {
			// The endpoint token is gateway routing metadata; the backend
			// would reject it as an unknown server argument.
			forward = StripEndpointOption(init)
		}
	}
	if endpointID == "" {
		s.fail(client, "no-endpoint", "08004",
			"could not identify endpoint: connect via your endpoint hostname or pass options=endpoint%3D<id>")
		return
	}

	route, ok := s.table.Lookup(endpointID)
	if !ok {
		s.fail(client, "unknown-endpoint", "08004", "endpoint not found: "+endpointID)
		return
	}
	if route.State == routes.StateSuspended {
		// Phase 4 replaces this with hold-and-wake.
		s.fail(client, "suspended", "57P03", "endpoint is suspended")
		return
	}
	if !s.acquire(endpointID, route.MaxConns) {
		s.fail(client, "too-many-connections", "53300",
			"too many connections for this endpoint")
		return
	}
	defer s.release(endpointID)

	backend, err := net.DialTimeout("tcp", route.Backend, s.cfg.DialTimeout)
	if err != nil {
		s.fail(client, "backend-dial", "08006", "endpoint backend unavailable")
		return
	}
	defer backend.Close()

	// Forward the already-consumed startup (or cancel) packet, then go
	// transparent.
	if _, err := backend.Write(forward); err != nil {
		s.fail(client, "backend-write", "08006", "endpoint backend unavailable")
		return
	}

	_ = client.SetDeadline(time.Time{}) // long-lived session from here on
	s.m.ConnsTotal.WithLabelValues(endpointID).Inc()
	s.m.ActiveConns.WithLabelValues(endpointID).Inc()
	defer s.m.ActiveConns.WithLabelValues(endpointID).Dec()

	s.pipe(client, backend, endpointID)
}

// pipe copies bytes both ways until either side closes.
func (s *Server) pipe(client, backend net.Conn, endpointID string) {
	done := make(chan struct{}, 2)
	copyCount := func(dst, src net.Conn, direction string) {
		n, _ := io.Copy(dst, src)
		s.m.Bytes.WithLabelValues(endpointID, direction).Add(float64(n))
		if half, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyCount(backend, client, "client_to_backend")
	go copyCount(client, backend, "backend_to_client")
	<-done
	<-done
}

func (s *Server) acquire(endpointID string, max int) bool {
	s.countM.Lock()
	defer s.countM.Unlock()
	if max > 0 && s.counts[endpointID] >= max {
		return false
	}
	s.counts[endpointID]++
	return true
}

func (s *Server) release(endpointID string) {
	s.countM.Lock()
	defer s.countM.Unlock()
	if s.counts[endpointID] > 0 {
		s.counts[endpointID]--
	}
}

// fail sends a Postgres error to the client (best effort) and records why.
func (s *Server) fail(w io.Writer, reason, sqlstate, message string) {
	s.m.ConnectErrors.WithLabelValues(reason).Inc()
	_, _ = w.Write(ErrorResponse(sqlstate, message))
	if s.log != nil {
		s.log.Debug("connection rejected", "reason", reason)
	}
}
