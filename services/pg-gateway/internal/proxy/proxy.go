// Package proxy implements the pg-gateway data path (ADR-007): terminate
// client TLS, route by SNI (or the options fallback) to the branch's backend,
// then become a transparent byte pipe. On a suspended endpoint it HOLDS the
// connection and triggers a wake (Phase 4, ADR-014), completing the connection
// once the branch is ready; if no waker is configured it rejects with a clear
// error as before.
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
	"github.com/zenulbashar/DB/services/pg-gateway/internal/wake"
)

// RouteTable is the gateway's view of the endpoint→backend routing table. The
// concrete *routes.Table satisfies it; tests use a fake that can flip a route
// from suspended to ready mid-hold.
type RouteTable interface {
	Lookup(endpointID string) (routes.Route, bool)
}

type Config struct {
	TLS *tls.Config
	// AllowInsecure permits non-TLS clients (tests / trusted in-cluster hops).
	// Production keeps this false: TLS-only endpoints (SECURITY_MODEL §5).
	AllowInsecure bool
	// DialTimeout bounds the backend dial.
	DialTimeout time.Duration
	// Waker triggers a wake for a suspended endpoint's branch. Nil disables
	// wake-on-connect — the gateway then rejects suspended endpoints (the
	// pre-Phase-4 behaviour).
	Waker wake.Waker
	// WakeTimeout bounds how long a connection is held waiting for its branch to
	// become ready (the honest Gen-1 cold-start budget; ADR-004 p95 < 25 s).
	WakeTimeout time.Duration
	// WakePoll is how often the held connection re-checks the route table for
	// readiness.
	WakePoll time.Duration
}

type Metrics struct {
	ActiveConns   *prometheus.GaugeVec
	ConnsTotal    *prometheus.CounterVec
	Bytes         *prometheus.CounterVec
	ConnectErrors *prometheus.CounterVec
	// Wake-on-connect (Phase 4).
	Wakes       *prometheus.CounterVec // by result: ready|timeout|error
	WakeWaitSec prometheus.Histogram   // hold duration until ready
	HoldsActive prometheus.Gauge       // connections currently held waiting to wake
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
		Wakes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "pggw_wakes_total", Help: "Wake-on-connect outcomes.",
		}, []string{"result"}),
		WakeWaitSec: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "pggw_wake_wait_seconds", Help: "Time a connection was held until its branch became ready.",
			// Buckets around the Gen-1 target (p50 < 10 s, p95 < 25 s; ADR-004).
			Buckets: []float64{0.5, 1, 2, 5, 10, 15, 20, 25, 30, 45},
		}),
		HoldsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "pggw_wake_holds_active", Help: "Connections currently held waiting for a wake.",
		}),
	}
	reg.MustRegister(m.ActiveConns, m.ConnsTotal, m.Bytes, m.ConnectErrors,
		m.Wakes, m.WakeWaitSec, m.HoldsActive)
	return m
}

type Server struct {
	cfg    Config
	table  RouteTable
	log    *slog.Logger
	m      *Metrics
	countM sync.Mutex
	counts map[string]int // per-endpoint (feeds the per-endpoint connection cap)
	// branchActive is the per-branch active connection count this gateway sees;
	// it is reported to the control plane for the suspend-on-idle decision
	// (ADR-015). Aggregation across gateway replicas happens control-plane-side.
	branchActive map[string]int
}

func New(cfg Config, table RouteTable, m *Metrics, log *slog.Logger) *Server {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.WakeTimeout == 0 {
		cfg.WakeTimeout = 30 * time.Second
	}
	if cfg.WakePoll == 0 {
		cfg.WakePoll = 500 * time.Millisecond
	}
	return &Server{cfg: cfg, table: table, log: log, m: m,
		counts: map[string]int{}, branchActive: map[string]int{}}
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
		ready, ok := s.holdAndWake(ctx, client, endpointID, route)
		if !ok {
			return // holdAndWake already answered the client
		}
		route = ready
	}
	if !s.acquire(endpointID, route.BranchID, route.MaxConns) {
		s.fail(client, "too-many-connections", "53300",
			"too many connections for this endpoint")
		return
	}
	defer s.release(endpointID, route.BranchID)

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

// holdAndWake handles a connection to a suspended endpoint (ADR-014): trigger a
// coalesced wake for its branch, then hold the connection — re-checking the
// route table — until the endpoint reports ready (proceed) or the wake budget
// expires (fail). Returns the now-ready route and true to continue, or false
// when it has already answered the client. With no waker configured, or a route
// missing its branch id, it falls back to the pre-Phase-4 clean rejection.
func (s *Server) holdAndWake(ctx context.Context, client net.Conn, endpointID string, route routes.Route) (routes.Route, bool) {
	if s.cfg.Waker == nil || route.BranchID == "" {
		s.fail(client, "suspended", "57P03", "endpoint is suspended")
		return route, false
	}
	s.m.HoldsActive.Inc()
	defer s.m.HoldsActive.Dec()
	// Hold the socket open across the wake budget; the startup-phase deadline
	// (30 s) would otherwise trip mid-wake.
	_ = client.SetDeadline(time.Now().Add(s.cfg.WakeTimeout + 10*time.Second))

	start := time.Now()
	if err := s.cfg.Waker.Wake(ctx, route.BranchID); err != nil {
		s.m.Wakes.WithLabelValues("error").Inc()
		if s.log != nil {
			s.log.Warn("wake trigger failed", "branch", route.BranchID, "err", err)
		}
		s.fail(client, "wake-failed", "57P03", "endpoint is suspended and could not be woken; retry shortly")
		return route, false
	}

	deadline := time.Now().Add(s.cfg.WakeTimeout)
	for {
		if r, ok := s.table.Lookup(endpointID); ok && r.State == routes.StateReady {
			s.m.Wakes.WithLabelValues("ready").Inc()
			s.m.WakeWaitSec.Observe(time.Since(start).Seconds())
			return r, true
		}
		if time.Now().After(deadline) {
			s.m.Wakes.WithLabelValues("timeout").Inc()
			s.fail(client, "wake-timeout", "57P03", "endpoint did not wake in time; retry shortly")
			return route, false
		}
		select {
		case <-ctx.Done():
			// Server shutting down; drop the held connection quietly.
			return route, false
		case <-time.After(s.cfg.WakePoll):
		}
	}
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

func (s *Server) acquire(endpointID, branchID string, max int) bool {
	s.countM.Lock()
	defer s.countM.Unlock()
	if max > 0 && s.counts[endpointID] >= max {
		return false
	}
	s.counts[endpointID]++
	if branchID != "" {
		s.branchActive[branchID]++
	}
	return true
}

func (s *Server) release(endpointID, branchID string) {
	s.countM.Lock()
	defer s.countM.Unlock()
	if s.counts[endpointID] > 0 {
		s.counts[endpointID]--
	}
	if branchID != "" && s.branchActive[branchID] > 0 {
		s.branchActive[branchID]--
	}
}

// BranchActivity snapshots the current per-branch active connection counts for
// reporting to the control plane (suspend-on-idle, ADR-015).
func (s *Server) BranchActivity() map[string]int {
	s.countM.Lock()
	defer s.countM.Unlock()
	out := make(map[string]int, len(s.branchActive))
	for k, v := range s.branchActive {
		out[k] = v
	}
	return out
}

// RunActivityReporter periodically reports this gateway's per-branch active
// connection counts to the control plane so it can drive the suspend-on-idle
// decision (ADR-015). Blocks until ctx is done. A nil reporter is a no-op.
func (s *Server) RunActivityReporter(ctx context.Context, reporter wake.Reporter, gatewayID string, interval time.Duration) {
	if reporter == nil {
		return
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			counts := s.BranchActivity()
			if len(counts) == 0 {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := reporter.Report(rctx, gatewayID, counts); err != nil && s.log != nil {
				s.log.Warn("activity report failed", "err", err)
			}
			cancel()
		}
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
