package auth

import (
	"strings"
	"testing"
)

func TestNewToken(t *testing.T) {
	token, hash, prefix := NewToken()

	if !strings.HasPrefix(token, TokenPrefix) {
		t.Fatalf("token missing prefix: %q", token)
	}
	if len(token) != len(TokenPrefix)+64 {
		t.Fatalf("token length = %d, want %d", len(token), len(TokenPrefix)+64)
	}
	if !WellFormed(token) {
		t.Fatal("freshly minted token must be well-formed")
	}
	if prefix != token[:PrefixLen] {
		t.Fatalf("prefix = %q, want first %d chars of token", prefix, PrefixLen)
	}
	if hash != HashToken(token) {
		t.Fatal("returned hash must match HashToken(token)")
	}
	if hash == token {
		t.Fatal("hash must not equal plaintext")
	}
}

func TestTokensAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		token, _, _ := NewToken()
		if seen[token] {
			t.Fatal("duplicate token generated")
		}
		seen[token] = true
	}
}

func TestWellFormed(t *testing.T) {
	cases := map[string]bool{
		"":                               false,
		"ndb_":                           false,
		"nbt_" + strings.Repeat("a", 64): false, // Nimbus prefix, not ours
		"ndb_" + strings.Repeat("a", 64): true,
		"ndb_" + strings.Repeat("g", 64): false, // non-hex
		"ndb_" + strings.Repeat("a", 63): false,
	}
	for in, want := range cases {
		if got := WellFormed(in); got != want {
			t.Errorf("WellFormed(%q) = %v, want %v", in, got, want)
		}
	}
}
