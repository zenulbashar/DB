package server

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zenulbashar/DB/services/control-plane/internal/secrets"
	"github.com/zenulbashar/DB/services/control-plane/internal/store/memory"
)

func serverWithKeyring(t *testing.T) *Server {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kr, err := secrets.ParseKeyring("1:"+base64.StdEncoding.EncodeToString(key), "")
	if err != nil {
		t.Fatal(err)
	}
	return New(memory.New(), Config{Keyring: kr, Version: "t"},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// The idempotency cache must never persist a credential in the clear (audit
// finding): create responses carry one-time API tokens and DB passwords.
func TestIdempotencyBodyEncryptedAtRest(t *testing.T) {
	s := serverWithKeyring(t)
	secret := []byte(`{"token":"zdb_deadbeefcafe","owner_role":{"password":"hunter2"}}`)

	stored := s.encodeIdem(secret)
	if len(stored) == 0 || stored[0] != 0x01 {
		t.Fatalf("expected encrypted (0x01) marker, got %v", stored[:1])
	}
	if bytes.Contains(stored, []byte("zdb_deadbeefcafe")) || bytes.Contains(stored, []byte("hunter2")) {
		t.Fatal("plaintext credential leaked into the cached idempotency body")
	}
	if got := s.decodeIdem(stored); !bytes.Equal(got, secret) {
		t.Fatalf("decode round-trip mismatch: %q", got)
	}
}

// Rows written before this fix carry a 0x00 marker and must still replay.
func TestIdempotencyDecodeLegacyPlaintext(t *testing.T) {
	s := serverWithKeyring(t)
	legacy := append([]byte{0x00}, []byte("hello")...)
	if got := s.decodeIdem(legacy); string(got) != "hello" {
		t.Fatalf("legacy decode = %q, want hello", got)
	}
}

// Same-key requests must serialize so two racing POSTs cannot both execute.
func TestKeyedMutexSerializes(t *testing.T) {
	km := newKeyedMutex()
	var inCrit, maxSeen int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := km.lock("same-key")
			defer unlock()
			n := atomic.AddInt32(&inCrit, 1)
			for {
				m := atomic.LoadInt32(&maxSeen)
				if n <= m || atomic.CompareAndSwapInt32(&maxSeen, m, n) {
					break
				}
			}
			atomic.AddInt32(&inCrit, -1)
		}()
	}
	wg.Wait()
	if maxSeen != 1 {
		t.Fatalf("critical section entered concurrently: max %d", maxSeen)
	}
	// Reference-counted cleanup must leave the map empty.
	if len(km.locks) != 0 {
		t.Fatalf("keyedMutex leaked %d entries", len(km.locks))
	}
}
