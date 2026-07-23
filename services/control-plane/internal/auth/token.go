// Package auth implements zdb_ API-key material handling per SECURITY_MODEL §3:
// 256-bit random tokens, SHA-256 hashed at rest, shown exactly once at creation.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

const (
	TokenPrefix = "zdb_"
	// PrefixLen is how many leading characters of the token are stored and
	// listed for identification (api/openapi.yaml ApiKey.prefix).
	PrefixLen = 12
)

// NewToken returns (token, sha256hex, displayPrefix).
func NewToken() (string, string, string) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand failure is unrecoverable for a credential issuer.
		panic("auth: crypto/rand unavailable: " + err.Error())
	}
	token := TokenPrefix + hex.EncodeToString(raw)
	return token, HashToken(token), token[:PrefixLen]
}

// HashToken hashes the full token string for at-rest storage and lookup.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// WellFormed cheaply rejects garbage before hashing/lookup.
func WellFormed(token string) bool {
	if !strings.HasPrefix(token, TokenPrefix) {
		return false
	}
	body := token[len(TokenPrefix):]
	if len(body) != 64 {
		return false
	}
	_, err := hex.DecodeString(body)
	return err == nil
}

// Equal is a constant-time hash comparison helper for tests and callers that
// compare hashes outside the database.
func Equal(hashA, hashB string) bool {
	return subtle.ConstantTimeCompare([]byte(hashA), []byte(hashB)) == 1
}
