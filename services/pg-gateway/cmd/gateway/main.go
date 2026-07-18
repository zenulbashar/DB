// Command gateway runs the NimbusDB Postgres TCP gateway (ADR-007).
package main

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/zenulbashar/DB/services/pg-gateway/internal/proxy"
	"github.com/zenulbashar/DB/services/pg-gateway/internal/routes"
	"github.com/zenulbashar/DB/services/pg-gateway/internal/wake"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	listenAddr := getenv("PGGW_LISTEN_ADDR", ":5432")
	metricsAddr := getenv("PGGW_METRICS_ADDR", ":9090")
	routesFile := getenv("PGGW_ROUTES_FILE", "/etc/pg-gateway/routes.json")
	certFile := os.Getenv("PGGW_TLS_CERT_FILE")
	keyFile := os.Getenv("PGGW_TLS_KEY_FILE")

	if certFile == "" || keyFile == "" {
		log.Error("PGGW_TLS_CERT_FILE and PGGW_TLS_KEY_FILE are required (TLS-only endpoints, SECURITY_MODEL §5)")
		os.Exit(1)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Error("load tls keypair", "err", err)
		os.Exit(1)
	}

	table, err := routes.Load(routesFile, 2*time.Second, log)
	if err != nil {
		log.Error("load route table", "err", err, "path", routesFile)
		os.Exit(1)
	}

	// Wake-on-connect (ADR-014): enabled only when both the control-plane URL
	// and the shared gateway token are configured; otherwise suspended endpoints
	// are cleanly rejected as before.
	var waker wake.Waker
	cpURL := os.Getenv("PGGW_CONTROL_PLANE_URL")
	gwToken := os.Getenv("PGGW_GATEWAY_TOKEN")
	wakeTimeout := getdur("PGGW_WAKE_TIMEOUT", 30*time.Second)
	if cpURL != "" && gwToken != "" {
		waker = wake.NewHTTP(cpURL, gwToken, 10*time.Second)
		log.Info("wake-on-connect enabled", "control_plane", cpURL, "wake_timeout", wakeTimeout)
	} else {
		log.Info("wake-on-connect disabled (set PGGW_CONTROL_PLANE_URL and PGGW_GATEWAY_TOKEN to enable)")
	}

	reg := prometheus.NewRegistry()
	m := proxy.NewMetrics(reg)
	srv := proxy.New(proxy.Config{
		TLS:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		Waker:       waker,
		WakeTimeout: wakeTimeout,
	}, table, m, log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		s := &http.Server{Addr: metricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() { <-ctx.Done(); _ = s.Close() }()
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("metrics server", "err", err)
		}
	}()

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Error("listen", "err", err)
		os.Exit(1)
	}
	log.Info("pg-gateway listening", "addr", listenAddr, "routes", table.Len())
	if err := srv.Serve(ctx, ln); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getdur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
