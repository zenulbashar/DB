package wake

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPWakerRequestShape(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := NewHTTP(srv.URL+"/", "secret-tok", 5*time.Second) // trailing slash must be trimmed
	if err := w.Wake(context.Background(), "br_01abc"); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/internal/branches/br_01abc/wake" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer secret-tok" {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestHTTPWakerNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := NewHTTP(srv.URL, "t", time.Second).Wake(context.Background(), "br_1"); err == nil {
		t.Fatal("expected an error on 500")
	}
}

// A connection storm to one suspended branch must produce exactly ONE wake POST
// (SECURITY_MODEL §2 coalescing).
func TestHTTPWakerCoalescesPerBranch(t *testing.T) {
	var count int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		<-release // hold the in-flight call so concurrent callers coalesce onto it
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := NewHTTP(srv.URL, "t", 5*time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = w.Wake(context.Background(), "br_same") }()
	}
	time.Sleep(100 * time.Millisecond) // let all 20 register on the singleflight key
	close(release)
	wg.Wait()
	if n := atomic.LoadInt32(&count); n != 1 {
		t.Fatalf("wake POSTs = %d, want 1 (coalesced)", n)
	}

	// Distinct branches are not coalesced together.
	atomic.StoreInt32(&count, 0)
	release2 := make(chan struct{})
	close(release2) // don't block this round
	_ = w.Wake(context.Background(), "br_a")
	_ = w.Wake(context.Background(), "br_b")
	if n := atomic.LoadInt32(&count); n != 2 {
		t.Fatalf("distinct-branch POSTs = %d, want 2", n)
	}
}

// A caller cancelling its context must not abort the shared wake for others.
func TestHTTPWakerIgnoresCallerCancellation(t *testing.T) {
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	if err := NewHTTP(srv.URL, "t", 5*time.Second).Wake(ctx, "br_1"); err != nil {
		t.Fatalf("wake with cancelled caller ctx = %v, want success (POST uses its own ctx)", err)
	}
	if atomic.LoadInt32(&count) != 1 {
		t.Fatal("wake POST was not sent")
	}
}
