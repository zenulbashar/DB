package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/zenulbashar/DB/services/pg-gateway/internal/routes"
)

func retryServer(t *testing.T) *Server {
	t.Helper()
	return New(Config{DialTimeout: 2 * time.Second},
		routes.NewStatic(map[string]routes.Route{}),
		NewMetrics(prometheus.NewRegistry()),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// reservePort grabs a free 127.0.0.1 port and releases it so the test can
// bring a listener up on it mid-retry.
func reservePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// A backend that comes up between the first and second attempt (a failover
// blip) is reached — the connection succeeds instead of erroring the client.
func TestDialBackendRetriesOverBlip(t *testing.T) {
	s := retryServer(t)
	addr := reservePort(t)

	// Bring the listener up ~100ms in — after attempt 1 fails (refused,
	// immediate) but comfortably before attempt 2 at ~250ms.
	go func() {
		time.Sleep(100 * time.Millisecond)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return // port raced away; the test will fail on dial and report it
		}
		conn, _ := ln.Accept()
		if conn != nil {
			defer conn.Close()
		}
		defer ln.Close()
		time.Sleep(time.Second)
	}()

	conn, err := s.dialBackend(context.Background(), addr)
	if err != nil {
		t.Fatalf("dialBackend over a blip = %v, want success", err)
	}
	conn.Close()
	if got := testutil.ToFloat64(s.m.DialRetries); got < 1 {
		t.Fatalf("dial retries metric = %v, want >= 1", got)
	}
}

// A permanently-down backend still fails — inside the bounded budget, after
// exhausting the schedule (3 attempts, 2 recorded retries).
func TestDialBackendBoundedFailure(t *testing.T) {
	s := retryServer(t)
	addr := reservePort(t) // nothing ever listens

	start := time.Now()
	_, err := s.dialBackend(context.Background(), addr)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("dialBackend to a dead backend must fail")
	}
	// Refused connections fail fast, so elapsed ≈ backoff sum (750ms); allow
	// generous slack but insist it is bounded (well under a client's timeout).
	if elapsed > 5*time.Second {
		t.Fatalf("bounded retry took %v, want < 5s", elapsed)
	}
	if got := testutil.ToFloat64(s.m.DialRetries); got != 2 {
		t.Fatalf("dial retries metric = %v, want 2 (3 attempts)", got)
	}
}

// A client giving up mid-backoff aborts the retry loop promptly.
func TestDialBackendAbortsOnContextCancel(t *testing.T) {
	s := retryServer(t)
	addr := reservePort(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond) // during the first 250ms backoff
		cancel()
	}()
	start := time.Now()
	_, err := s.dialBackend(ctx, addr)
	if err == nil {
		t.Fatal("canceled dialBackend must fail")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cancel took %v to unwind, want prompt", time.Since(start))
	}
}
