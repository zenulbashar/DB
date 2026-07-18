package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/zenulbashar/DB/services/pg-gateway/internal/routes"
)

type fakeTable struct {
	mu sync.Mutex
	m  map[string]routes.Route
}

func (f *fakeTable) Lookup(id string) (routes.Route, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.m[id]
	return r, ok
}
func (f *fakeTable) set(id string, r routes.Route) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[id] = r
}

type fakeWaker struct {
	mu    sync.Mutex
	calls map[string]int
	err   error
}

func (f *fakeWaker) Wake(_ context.Context, branchID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[branchID]++
	return f.err
}
func (f *fakeWaker) count(b string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[b] }

func testServer(t *testing.T, cfg Config, table RouteTable) *Server {
	t.Helper()
	return New(cfg, table, NewMetrics(prometheus.NewRegistry()), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// drain reads and discards everything the gateway writes to the client end, so
// the synchronous net.Pipe never blocks a fail-response write.
func drain(c net.Conn) { go func() { _, _ = io.Copy(io.Discard, c) }() }

func TestHoldAndWakeProceedsWhenReady(t *testing.T) {
	ft := &fakeTable{m: map[string]routes.Route{
		"ep_1": {State: routes.StateSuspended, BranchID: "br_1", Backend: "b:5432"},
	}}
	fw := &fakeWaker{}
	s := testServer(t, Config{Waker: fw, WakeTimeout: 2 * time.Second, WakePoll: 5 * time.Millisecond}, ft)

	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	drain(peer)

	// Simulate the reconciler bringing the branch up shortly after the wake.
	go func() {
		time.Sleep(30 * time.Millisecond)
		ft.set("ep_1", routes.Route{State: routes.StateReady, BranchID: "br_1", Backend: "b:5432"})
	}()

	route, _ := ft.Lookup("ep_1")
	got, ok := s.holdAndWake(context.Background(), client, "ep_1", route)
	if !ok || got.State != routes.StateReady {
		t.Fatalf("holdAndWake = (%+v, %v), want ready/true", got, ok)
	}
	if fw.count("br_1") != 1 {
		t.Fatalf("wake called %d times, want 1", fw.count("br_1"))
	}
}

func TestHoldAndWakeTimesOut(t *testing.T) {
	ft := &fakeTable{m: map[string]routes.Route{
		"ep_1": {State: routes.StateSuspended, BranchID: "br_1", Backend: "b:5432"},
	}}
	fw := &fakeWaker{}
	s := testServer(t, Config{Waker: fw, WakeTimeout: 40 * time.Millisecond, WakePoll: 5 * time.Millisecond}, ft)

	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	drain(peer)

	route, _ := ft.Lookup("ep_1")
	_, ok := s.holdAndWake(context.Background(), client, "ep_1", route)
	if ok {
		t.Fatal("holdAndWake should have timed out (branch never became ready)")
	}
	if fw.count("br_1") != 1 {
		t.Fatalf("wake called %d times, want 1", fw.count("br_1"))
	}
}

func TestHoldAndWakeRejectsWithoutWaker(t *testing.T) {
	ft := &fakeTable{m: map[string]routes.Route{
		"ep_1": {State: routes.StateSuspended, BranchID: "br_1"},
	}}
	s := testServer(t, Config{}, ft) // no waker

	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	drain(peer)

	route, _ := ft.Lookup("ep_1")
	if _, ok := s.holdAndWake(context.Background(), client, "ep_1", route); ok {
		t.Fatal("with no waker configured a suspended endpoint must be rejected")
	}
}

func TestHoldAndWakeRejectsWithoutBranchID(t *testing.T) {
	ft := &fakeTable{m: map[string]routes.Route{
		"ep_1": {State: routes.StateSuspended, BranchID: ""}, // route not yet carrying branch_id
	}}
	fw := &fakeWaker{}
	s := testServer(t, Config{Waker: fw, WakeTimeout: time.Second, WakePoll: 5 * time.Millisecond}, ft)

	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	drain(peer)

	route, _ := ft.Lookup("ep_1")
	if _, ok := s.holdAndWake(context.Background(), client, "ep_1", route); ok {
		t.Fatal("a route without a branch_id is not wakeable")
	}
	if fw.count("br_1") != 0 {
		t.Fatal("must not trigger a wake without a branch id")
	}
}

func TestHoldAndWakeFailsWhenWakeErrors(t *testing.T) {
	ft := &fakeTable{m: map[string]routes.Route{
		"ep_1": {State: routes.StateSuspended, BranchID: "br_1"},
	}}
	fw := &fakeWaker{err: io.ErrUnexpectedEOF}
	s := testServer(t, Config{Waker: fw, WakeTimeout: time.Second, WakePoll: 5 * time.Millisecond}, ft)

	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	drain(peer)

	route, _ := ft.Lookup("ep_1")
	if _, ok := s.holdAndWake(context.Background(), client, "ep_1", route); ok {
		t.Fatal("a failed wake trigger must not proceed")
	}
}

func TestHoldAndWakeStopsOnContextCancel(t *testing.T) {
	ft := &fakeTable{m: map[string]routes.Route{
		"ep_1": {State: routes.StateSuspended, BranchID: "br_1"},
	}}
	fw := &fakeWaker{}
	s := testServer(t, Config{Waker: fw, WakeTimeout: 5 * time.Second, WakePoll: 5 * time.Millisecond}, ft)

	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	drain(peer)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	route, _ := ft.Lookup("ep_1")
	done := make(chan bool, 1)
	go func() {
		_, ok := s.holdAndWake(ctx, client, "ep_1", route)
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("expected holdAndWake to abort on context cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("holdAndWake did not return after context cancel")
	}
}
