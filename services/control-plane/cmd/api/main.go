// Command api runs the Zale DB control-plane REST API.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zenulbashar/DB/services/control-plane/internal/config"
	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/secrets"
	"github.com/zenulbashar/DB/services/control-plane/internal/server"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "apply migrations and exit")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	domain.SetBaseDomain(cfg.Domain)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if bypass, err := postgres.RLSBypassed(ctx, st.Pool()); err != nil {
		log.Error("rls check", "err", err)
		os.Exit(1)
	} else if bypass {
		log.Error("refusing to start: DATABASE_URL role is superuser or BYPASSRLS; " +
			"row-level security would be silently bypassed. Use a dedicated non-superuser role " +
			"(local dev: ndb_app via docker-compose init script).")
		os.Exit(1)
	}

	if err := postgres.Migrate(ctx, st.Pool()); err != nil {
		log.Error("migrate", "err", err)
		os.Exit(1)
	}
	if *migrateOnly {
		log.Info("migrations applied")
		return
	}

	keyring, err := secrets.ParseKeyring(cfg.KEKs, cfg.ActiveKEK)
	if err != nil {
		log.Error("keyring", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr: ":" + cfg.Port,
		Handler: server.New(st, server.Config{
			BootstrapToken: cfg.BootstrapToken, GatewayToken: cfg.GatewayToken,
			AdminToken: cfg.AdminToken,
			Version:    cfg.Version, Keyring: keyring,
		}, log),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("control-plane api listening", "port", cfg.Port, "env", cfg.Env)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}
