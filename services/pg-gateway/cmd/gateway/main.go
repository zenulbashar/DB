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

	reg := prometheus.NewRegistry()
	m := proxy.NewMetrics(reg)
	srv := proxy.New(proxy.Config{
		TLS: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
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
