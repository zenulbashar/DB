// Command reconciler runs the NimbusDB data-plane reconciler: it converges
// desired state from the control-plane database into CNPG/Kubernetes objects
// and publishes the gateway route table.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	appcfg "github.com/zenulbashar/DB/services/control-plane/internal/config"
	"github.com/zenulbashar/DB/services/control-plane/internal/reconciler"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

func getenvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := appcfg.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	interval := 10 * time.Second
	if v := os.Getenv("NDB_RECONCILE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			interval = d
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

	restCfg, err := config.GetConfig() // in-cluster or KUBECONFIG
	if err != nil {
		log.Error("kubeconfig", "err", err)
		os.Exit(1)
	}
	kc, err := client.New(restCfg, client.Options{Scheme: reconciler.Scheme()})
	if err != nil {
		log.Error("k8s client", "err", err)
		os.Exit(1)
	}

	var backup *reconciler.BackupConfig
	if bucket := os.Getenv("NDB_BACKUP_BUCKET"); bucket != "" {
		backup = &reconciler.BackupConfig{
			BucketBase:        bucket,
			EndpointURL:       os.Getenv("NDB_BACKUP_ENDPOINT_URL"),
			CredentialsSecret: getenvDefault("NDB_BACKUP_CREDENTIALS_SECRET", "ndb-backup-credentials"),
		}
	} else if os.Getenv("NDB_ENV") != "dev" {
		log.Error("NDB_BACKUP_BUCKET is required outside dev: tenant clusters must archive WAL (DATABASE_ARCHITECTURE §3, risk R-2)")
		os.Exit(1)
	} else {
		log.Warn("running WITHOUT WAL archiving/backups (dev only)")
	}

	engine := reconciler.New(st, kc, backup, log)

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		s := &http.Server{Addr: ":8081", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() { <-ctx.Done(); _ = s.Close() }()
		_ = s.ListenAndServe()
	}()

	// Restore-verification loop (R-2): opt-in via NDB_VERIFY_INTERVAL (e.g.
	// "24h"); needs an archive to verify, so it also requires backups on.
	if v := os.Getenv("NDB_VERIFY_INTERVAL"); v != "" && backup != nil {
		verifyEvery, err := time.ParseDuration(v)
		if err != nil || verifyEvery <= 0 {
			log.Error("bad NDB_VERIFY_INTERVAL", "value", v)
			os.Exit(1)
		}
		verifyTimeout := 30 * time.Minute
		if tv := os.Getenv("NDB_VERIFY_TIMEOUT"); tv != "" {
			if d, err := time.ParseDuration(tv); err == nil && d > 0 {
				verifyTimeout = d
			}
		}
		// Settle/start checks run every reconcile-ish tick; verifyEvery is the
		// per-branch re-verification window, not the loop cadence.
		go func() {
			tick := time.NewTicker(interval * 6)
			defer tick.Stop()
			log.Info("restore-verification loop running",
				"every", verifyEvery.String(), "timeout", verifyTimeout.String())
			for {
				if err := engine.VerifyRestores(ctx, verifyEvery, verifyTimeout); err != nil {
					log.Error("restore verification pass", "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}()
	}

	log.Info("reconciler running", "interval", interval.String())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := engine.ReconcileOnce(ctx); err != nil {
			log.Error("reconcile pass", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
