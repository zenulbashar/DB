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
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

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
	workerID string
	leaseTTL time.Duration
}

// DefaultLeaseTTL bounds how long a claimed-but-silent worker holds an import
// before another replica may resume it. It must exceed the longest single
// stage (a large dump/restore) so a healthy-but-busy worker is never stolen
// from; a crashed worker's import waits at most this long to be picked up.
const DefaultLeaseTTL = 30 * time.Minute

func NewAdapter(st *postgres.Store, keyring *secrets.Keyring, resolver TargetResolver, opts ...Option) *Adapter {
	a := &Adapter{store: st, keyring: keyring, resolver: resolver,
		workerID: defaultWorkerID(), leaseTTL: DefaultLeaseTTL}
	for _, o := range opts {
		o(a)
	}
	return a
}

type Option func(*Adapter)

func WithWorkerID(id string) Option       { return func(a *Adapter) { a.workerID = id } }
func WithLeaseTTL(d time.Duration) Option { return func(a *Adapter) { a.leaseTTL = d } }

func defaultWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

var _ runner.ControlPlane = (*Adapter)(nil)

// Claim leases and returns the next actionable import as a runner.Job, or nil
// when nothing is claimable. The source URL is decrypted here, in the worker's
// memory, and never leaves it except as a connection to the source database.
func (a *Adapter) Claim(ctx context.Context) (*runner.Job, error) {
	c, err := a.store.ClaimActionableImport(ctx, a.workerID, a.leaseTTL)
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

// urlToConnInfo converts a postgres:// URL into a libpq conninfo the target
// server uses to reach the source during logical replication. Each value is
// quoted/escaped per libpq rules so a password (or any field) containing a
// space, quote, or backslash cannot break the conninfo or inject a keyword
// (audit finding). A parse failure returns a fixed, credential-free error —
// the raw URL must never ride out inside a *url.Error (audit finding).
func urlToConnInfo(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("source url: parse failed")
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	pairs := [][2]string{
		{"host", u.Hostname()},
		{"port", port},
		{"dbname", strings.TrimPrefix(u.Path, "/")},
	}
	if user := u.User.Username(); user != "" {
		pairs = append(pairs, [2]string{"user", user})
	}
	if pw, ok := u.User.Password(); ok {
		pairs = append(pairs, [2]string{"password", pw})
	}
	if sslmode := u.Query().Get("sslmode"); sslmode != "" {
		pairs = append(pairs, [2]string{"sslmode", sslmode})
	}
	var b strings.Builder
	for i, kv := range pairs {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(kv[0])
		b.WriteByte('=')
		b.WriteString(quoteConnInfo(kv[1]))
	}
	return b.String(), nil
}

// quoteConnInfo wraps a value in single quotes when it is empty or contains
// whitespace/quote/backslash, escaping backslashes and single quotes (libpq
// keyword/value rules).
func quoteConnInfo(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t\n\r'\\") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + r.Replace(v) + "'"
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
		dbs, err := st.ListDatabases(ctx, c.OrgID, br.ID)
		if err != nil {
			return "", err
		}
		roles, err := st.ListDBRoles(ctx, c.OrgID, br.ID)
		if err != nil {
			return "", err
		}
		if len(dbs) == 0 || len(roles) == 0 {
			return "", fmt.Errorf("target branch has no owner role or database yet")
		}
		// Connect as the database's ACTUAL owner role, matched by id — not an
		// arbitrary roles[0]/dbs[0] pairing (audit finding: list order is not
		// guaranteed and the first role may not own the first database).
		db := dbs[0]
		ownerName := ""
		for _, r := range roles {
			if r.ID == db.OwnerRoleID {
				ownerName = r.Name
			}
		}
		if ownerName == "" {
			return "", fmt.Errorf("owner role for database %q not found", db.Name)
		}
		ciphertext, err := st.GetDBRoleSecret(ctx, c.OrgID, br.ID, ownerName)
		if err != nil {
			return "", err
		}
		pw, err := keyring.Decrypt(ciphertext)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("postgresql://%s:%s@%s/%s?sslmode=require",
			ownerName, string(pw), host, db.Name), nil
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
