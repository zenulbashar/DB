// Package importworker adapts the platform's import runner
// (services/import-engine) onto the control-plane store. It is the SECURE
// wiring for migrations: the worker is a platform component with direct
// database + keyring access, so decrypted source credentials never traverse
// the tenant HTTP API (SECURITY_MODEL §5, audit follow-up). It implements
// runner.ControlPlane by claiming actionable imports from the store,
// decrypting the source URL, resolving the target connection, and persisting
// state transitions through the same state machine the API enforces.
package importworker

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/zenulbashar/DB/services/import-engine/runner"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/secrets"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/postgres"
)

// TargetResolver produces the URL a worker dials to reach the import's TARGET
// branch database. Injected so tests can point at local databases and
// production can assemble the branch's direct endpoint + a platform import
// role. (The libpq conninfo the target server uses to reach the source during
// logical replication is derived from the source URL, not here.)
type TargetResolver func(ctx context.Context, c *store.ClaimedImport) (targetURL string, err error)

type Adapter struct {
	store    *postgres.Store
	keyring  *secrets.Keyring
	resolver TargetResolver
}

func NewAdapter(st *postgres.Store, keyring *secrets.Keyring, resolver TargetResolver) *Adapter {
	return &Adapter{store: st, keyring: keyring, resolver: resolver}
}

var _ runner.ControlPlane = (*Adapter)(nil)

// Claim returns the next actionable import as a runner.Job, or nil when the
// queue is empty. The source URL is decrypted here, in the worker's memory,
// and never leaves it except as a connection to the source database.
func (a *Adapter) Claim(ctx context.Context) (*runner.Job, error) {
	c, err := a.store.ClaimActionableImport(ctx)
	if err != nil || c == nil {
		return nil, err
	}
	sourceURL, err := a.keyring.Decrypt(c.SourceCiphertext)
	if err != nil {
		// A job we cannot decrypt can never make progress; fail it so the
		// queue does not wedge on it.
		msg := fmt.Sprintf("decrypt source credential: %v", err)
		_, _ = a.store.TransitionImportByID(ctx, c.ImportID,
			store.TransitionImportParams{To: "failed", Error: &msg})
		return nil, fmt.Errorf("import %s: %s", c.ImportID, msg)
	}
	targetURL, err := a.resolver(ctx, c)
	if err != nil {
		msg := fmt.Sprintf("resolve target: %v", err)
		_, _ = a.store.TransitionImportByID(ctx, c.ImportID,
			store.TransitionImportParams{To: "failed", Error: &msg})
		return nil, fmt.Errorf("import %s: %s", c.ImportID, msg)
	}
	connInfo, err := urlToConnInfo(string(sourceURL))
	if err != nil {
		return nil, fmt.Errorf("import %s: source url: %w", c.ImportID, err)
	}
	return &runner.Job{
		ID:             c.ImportID,
		Mode:           string(c.Mode),
		SourceKind:     string(c.SourceKind),
		State:          string(c.State),
		SourceURL:      string(sourceURL),
		TargetURL:      targetURL,
		TargetConnInfo: connInfo,
	}, nil
}

// urlToConnInfo converts a postgres:// URL into the space-separated libpq
// conninfo the target server uses to reach the source during logical
// replication.
func urlToConnInfo(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	db := strings.TrimPrefix(u.Path, "/")
	info := fmt.Sprintf("host=%s port=%s dbname=%s", host, port, db)
	if user := u.User.Username(); user != "" {
		info += " user=" + user
	}
	if pw, ok := u.User.Password(); ok {
		info += " password=" + pw
	}
	if sslmode := u.Query().Get("sslmode"); sslmode != "" {
		info += " sslmode=" + sslmode
	}
	return info, nil
}

func toImportState(s string) domain.ImportState { return domain.ImportState(s) }

// ProductionTargetResolver assembles the target URL from the branch's
// rw_direct endpoint and its seeded owner role (through the gateway in prod).
// Requires the data plane to be live; local tests inject a resolver that
// returns a reachable database URL instead.
func ProductionTargetResolver(st *postgres.Store, keyring *secrets.Keyring) TargetResolver {
	return func(ctx context.Context, c *store.ClaimedImport) (string, error) {
		if c.TargetBranchID == nil {
			return "", fmt.Errorf("import has no target branch")
		}
		br, err := st.GetBranch(ctx, c.OrgID, *c.TargetBranchID)
		if err != nil {
			return "", err
		}
		var host string
		for _, ep := range br.Endpoints {
			if ep.Kind == domain.EndpointRWDirect {
				host = ep.Host
			}
		}
		if host == "" {
			return "", fmt.Errorf("branch has no direct endpoint")
		}
		roles, err := st.ListDBRoles(ctx, c.OrgID, br.ID)
		if err != nil {
			return "", err
		}
		dbs, err := st.ListDatabases(ctx, c.OrgID, br.ID)
		if err != nil {
			return "", err
		}
		if len(roles) == 0 || len(dbs) == 0 {
			return "", fmt.Errorf("target branch has no owner role or database yet")
		}
		ciphertext, err := st.GetDBRoleSecret(ctx, c.OrgID, br.ID, roles[0].Name)
		if err != nil {
			return "", err
		}
		pw, err := keyring.Decrypt(ciphertext)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("postgresql://%s:%s@%s/%s?sslmode=require",
			roles[0].Name, string(pw), host, dbs[0].Name), nil
	}
}

// Transition persists a state-machine step (privileged; the worker spans
// tenants by design).
func (a *Adapter) Transition(ctx context.Context, id, to string, report, checkpoints map[string]any, errMsg *string) error {
	_, err := a.store.TransitionImportByID(ctx, id, store.TransitionImportParams{
		To:          toImportState(to),
		Report:      report,
		Checkpoints: checkpoints,
		Error:       errMsg,
	})
	return err
}
