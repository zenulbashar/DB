//go:build integration

// End-to-end gateway test: a real pgx client connects through the gateway
// with TLS + SNI (and the options fallback) to a live Postgres.
//
//	TEST_DATABASE_URL=postgres://user:pass@host:port/db go test -tags=integration ./internal/proxy/
package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/zenulbashar/DB/services/pg-gateway/internal/routes"
)

const testEndpoint = "ep_e2etest"
const testSNI = "ep-e2etest.syd1.db.nimbus.app"

type testEnv struct {
	addr    string // gateway listen addr
	connCfg *pgx.ConnConfig
}

func selfSignedWildcard(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "*.syd1.db.nimbus.app"},
		DNSNames:     []string{"*.syd1.db.nimbus.app"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func startGateway(t *testing.T, maxConns int) *testEnv {
	t.Helper()
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping gateway e2e")
	}
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	backend := u.Host
	if !strings.Contains(backend, ":") {
		backend += ":5432"
	}

	table := routes.NewStatic(map[string]routes.Route{
		testEndpoint: {Backend: backend, State: routes.StateReady, MaxConns: maxConns},
		"ep_asleep":  {Backend: backend, State: routes.StateSuspended},
	})
	m := NewMetrics(prometheus.NewRegistry())
	srv := New(Config{
		TLS: &tls.Config{Certificates: []tls.Certificate{selfSignedWildcard(t)}, MinVersion: tls.VersionTLS12},
	}, table, m, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Serve(ctx, ln)

	// Client config: TCP to the gateway, TLS with the endpoint's SNI.
	cfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg.Host = host
	cfg.Port = mustPort(t, port)
	cfg.TLSConfig = &tls.Config{ServerName: testSNI, InsecureSkipVerify: true}
	// pgx fallbacks would retry without TLS; keep the single TLS attempt.
	cfg.Fallbacks = nil
	return &testEnv{addr: ln.Addr().String(), connCfg: cfg}
}

func mustPort(t *testing.T, p string) uint16 {
	t.Helper()
	var n int
	for _, c := range p {
		n = n*10 + int(c-'0')
	}
	return uint16(n)
}

func TestGatewayRoutesBySNI(t *testing.T) {
	env := startGateway(t, 0)
	ctx := context.Background()

	conn, err := pgx.ConnectConfig(ctx, env.connCfg)
	if err != nil {
		t.Fatalf("connect through gateway: %v", err)
	}
	defer conn.Close(ctx)

	var one int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 through gateway: %v (%d)", err, one)
	}
	// A parameterized round trip exercises the extended protocol end-to-end.
	var echo string
	if err := conn.QueryRow(ctx, "SELECT $1::text", "nimbusdb").Scan(&echo); err != nil || echo != "nimbusdb" {
		t.Fatalf("extended-protocol roundtrip: %v (%q)", err, echo)
	}
}

func TestGatewayOptionsFallback(t *testing.T) {
	env := startGateway(t, 0)
	ctx := context.Background()

	cfg := env.connCfg.Copy()
	// Non-endpoint SNI: routing must come from options=endpoint%3D…
	cfg.TLSConfig = &tls.Config{ServerName: "gateway.internal", InsecureSkipVerify: true}
	cfg.RuntimeParams["options"] = "endpoint=" + testEndpoint

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect via options fallback: %v", err)
	}
	defer conn.Close(ctx)
	var one int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayRejectsUnknownEndpoint(t *testing.T) {
	env := startGateway(t, 0)
	cfg := env.connCfg.Copy()
	cfg.TLSConfig = &tls.Config{ServerName: "ep-nosuch.syd1.db.nimbus.app", InsecureSkipVerify: true}

	_, err := pgx.ConnectConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("unknown endpoint must be rejected")
	}
	if !strings.Contains(err.Error(), "endpoint not found") {
		t.Fatalf("want 'endpoint not found' error, got: %v", err)
	}
}

func TestGatewayRejectsSuspendedEndpoint(t *testing.T) {
	env := startGateway(t, 0)
	cfg := env.connCfg.Copy()
	cfg.TLSConfig = &tls.Config{ServerName: "ep-asleep.syd1.db.nimbus.app", InsecureSkipVerify: true}

	_, err := pgx.ConnectConfig(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "suspended") {
		t.Fatalf("want 'suspended' error, got: %v", err)
	}
}

func TestGatewayEnforcesConnectionCap(t *testing.T) {
	env := startGateway(t, 1)
	ctx := context.Background()

	first, err := pgx.ConnectConfig(ctx, env.connCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(ctx)

	_, err = pgx.ConnectConfig(ctx, env.connCfg)
	if err == nil || !strings.Contains(err.Error(), "too many connections") {
		t.Fatalf("want connection-cap rejection, got: %v", err)
	}

	// Freeing the slot admits the next client.
	first.Close(ctx)
	time.Sleep(100 * time.Millisecond)
	third, err := pgx.ConnectConfig(ctx, env.connCfg)
	if err != nil {
		t.Fatalf("connect after release: %v", err)
	}
	third.Close(ctx)
}

func TestGatewayRejectsPlaintext(t *testing.T) {
	env := startGateway(t, 0)
	cfg := env.connCfg.Copy()
	cfg.TLSConfig = nil // sslmode=disable

	_, err := pgx.ConnectConfig(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "SSL required") {
		t.Fatalf("want 'SSL required' rejection, got: %v", err)
	}
}
