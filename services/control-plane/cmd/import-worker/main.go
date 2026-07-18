// Command import-worker drives database migrations to completion. It is a
// platform component with direct control-plane database + keyring access, so
// decrypted source credentials never traverse the tenant API. It claims
// actionable imports, drives one migration stage per tick through the shared
// import runner, and persists state transitions.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zenulbashar/DB/services/import-engine/runner"

	"github.com/zenulbashar/DB/services/control-plane/internal/config"
	"github.com/zenulbashar/DB/services/control-plane/internal/importworker"
	"github.com/zenulbashar/DB/services/control-plane/internal/secrets"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	keyring, err := secrets.ParseKeyring(cfg.KEKs, cfg.ActiveKEK)
	if err != nil {
		log.Error("keyring", "err", err)
		os.Exit(1)
	}
	poll := 3 * time.Second
	if v := os.Getenv("NDB_IMPORT_POLL"); v != "" {
		if d, perr := time.ParseDuration(v); perr == nil {
			poll = d
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := postgres.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	adapter := importworker.NewAdapter(st, keyring, importworker.ProductionTargetResolver(st, keyring))
	r := runner.New(adapter, runner.Options{})

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		s := &http.Server{Addr: ":8082", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() { <-ctx.Done(); _ = s.Close() }()
		_ = s.ListenAndServe()
	}()

	log.Info("import-worker running", "poll", poll.String())
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		// Drain: keep stepping while there is actionable work, then idle.
		for {
			job, err := r.Step(ctx)
			if err != nil {
				log.Error("import step", "err", err)
				break
			}
			if job == nil {
				break
			}
			log.Info("advanced import", "id", job.ID, "from_state", job.State)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
