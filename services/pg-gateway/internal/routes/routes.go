// Package routes holds the gateway's endpoint → backend routing table.
// v1 sources it from a JSON file re-polled on mtime change; the Phase 2c
// reconciler writes that file (a mounted ConfigMap in-cluster). The gateway
// keeps serving the last good table if a reload fails — routing must survive
// control-plane outages (SYSTEM_ARCHITECTURE §5).
package routes

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

type State string

const (
	StateReady     State = "ready"
	StateSuspended State = "suspended"
)

type Route struct {
	// Backend is the upstream host:port (the CNPG rw service or pooler).
	Backend string `json:"backend"`
	State   State  `json:"state"`
	// MaxConns caps concurrent client connections through the gateway for
	// this endpoint; 0 means unlimited.
	MaxConns int `json:"max_conns"`
	// BranchID identifies the branch to wake when this endpoint is suspended
	// (wake-on-connect trigger; ADR-014). Empty until the reconciler that
	// populates it is deployed — the gateway treats an empty value as
	// "not wakeable" and falls back to the suspended rejection.
	BranchID string `json:"branch_id"`
}

type tableFile struct {
	Endpoints map[string]Route `json:"endpoints"`
}

type Table struct {
	mu    sync.RWMutex
	byID  map[string]Route
	path  string
	mtime time.Time
	log   *slog.Logger
}

func NewStatic(endpoints map[string]Route) *Table {
	return &Table{byID: endpoints}
}

// Load reads the table from path and starts a background poller that reloads
// on mtime change every interval.
func Load(path string, interval time.Duration, log *slog.Logger) (*Table, error) {
	t := &Table{byID: map[string]Route{}, path: path, log: log}
	if err := t.reload(); err != nil {
		return nil, err
	}
	go t.poll(interval)
	return t, nil
}

func (t *Table) Lookup(endpointID string) (Route, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	r, ok := t.byID[endpointID]
	return r, ok
}

func (t *Table) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.byID)
}

func (t *Table) reload() error {
	st, err := os.Stat(t.path)
	if err != nil {
		return err
	}
	t.mu.RLock()
	unchanged := st.ModTime().Equal(t.mtime)
	t.mu.RUnlock()
	if unchanged {
		return nil
	}
	b, err := os.ReadFile(t.path)
	if err != nil {
		return err
	}
	var f tableFile
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	if f.Endpoints == nil {
		f.Endpoints = map[string]Route{}
	}
	t.mu.Lock()
	t.byID = f.Endpoints
	t.mtime = st.ModTime()
	t.mu.Unlock()
	if t.log != nil {
		t.log.Info("route table reloaded", "endpoints", len(f.Endpoints))
	}
	return nil
}

func (t *Table) poll(interval time.Duration) {
	for {
		time.Sleep(interval)
		if err := t.reload(); err != nil && t.log != nil {
			// Keep serving the previous table; a broken write must not take
			// down routing.
			t.log.Error("route table reload failed; keeping last good table", "err", err)
		}
	}
}
