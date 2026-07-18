// Package secrets implements envelope encryption for tenant secret material
// (SECURITY_MODEL §5): each secret gets a fresh AES-256-GCM data key (DEK),
// wrapped by a versioned key-encryption key (KEK). v1 sources KEKs from the
// environment; a cloud KMS replaces the keyring without changing the blob
// format (the KEK version field is the seam).
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	blobVersion   = 1
	dekLen        = 32
	gcmNonceLen   = 12
	gcmTagLen     = 16
	wrappedDEKLen = dekLen + gcmTagLen
)

var (
	ErrMalformedBlob = errors.New("secrets: malformed ciphertext blob")
	ErrUnknownKEK    = errors.New("secrets: unknown KEK version")
)

// Keyring holds KEKs by version; Active is used for new encryptions, older
// versions remain decryptable until rotation re-wraps them.
type Keyring struct {
	active uint32
	keys   map[uint32][]byte
}

// ParseKeyring reads "1:<base64>,2:<base64>" (NDB_KEKS) and the active
// version (NDB_ACTIVE_KEK; defaults to the highest version present).
func ParseKeyring(keksEnv, activeEnv string) (*Keyring, error) {
	if strings.TrimSpace(keksEnv) == "" {
		return nil, errors.New("secrets: NDB_KEKS is required (format \"1:<base64 32-byte key>\")")
	}
	kr := &Keyring{keys: map[uint32][]byte{}}
	var highest uint32
	for _, part := range strings.Split(keksEnv, ",") {
		verStr, b64, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			return nil, fmt.Errorf("secrets: bad KEK entry %q", part)
		}
		var ver uint32
		if _, err := fmt.Sscanf(verStr, "%d", &ver); err != nil || ver == 0 {
			return nil, fmt.Errorf("secrets: bad KEK version %q", verStr)
		}
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(key) != dekLen {
			return nil, fmt.Errorf("secrets: KEK v%d must be base64 of exactly 32 bytes", ver)
		}
		kr.keys[ver] = key
		if ver > highest {
			highest = ver
		}
	}
	kr.active = highest
	if activeEnv != "" {
		var ver uint32
		if _, err := fmt.Sscanf(activeEnv, "%d", &ver); err != nil {
			return nil, fmt.Errorf("secrets: bad NDB_ACTIVE_KEK %q", activeEnv)
		}
		if _, ok := kr.keys[ver]; !ok {
			return nil, fmt.Errorf("%w: active v%d not in keyring", ErrUnknownKEK, ver)
		}
		kr.active = ver
	}
	return kr, nil
}

func (k *Keyring) ActiveVersion() uint32 { return k.active }

// Encrypt seals plaintext under a fresh DEK wrapped by the active KEK.
// Blob layout: ver(1) | kekVer(4) | dekNonce(12) | wrappedDEK(48) |
// secretNonce(12) | sealed(len+16).
func (k *Keyring) Encrypt(plaintext []byte) ([]byte, uint32, error) {
	kek := k.keys[k.active]

	dek := make([]byte, dekLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, 0, err
	}
	wrapNonce, wrapped, err := seal(kek, dek)
	if err != nil {
		return nil, 0, err
	}
	secNonce, sealed, err := seal(dek, plaintext)
	if err != nil {
		return nil, 0, err
	}

	blob := make([]byte, 0, 1+4+gcmNonceLen+wrappedDEKLen+gcmNonceLen+len(sealed))
	blob = append(blob, blobVersion)
	blob = binary.BigEndian.AppendUint32(blob, k.active)
	blob = append(blob, wrapNonce...)
	blob = append(blob, wrapped...)
	blob = append(blob, secNonce...)
	blob = append(blob, sealed...)
	return blob, k.active, nil
}

// Decrypt opens a blob produced by Encrypt with any keyring version.
func (k *Keyring) Decrypt(blob []byte) ([]byte, error) {
	minLen := 1 + 4 + gcmNonceLen + wrappedDEKLen + gcmNonceLen + gcmTagLen
	if len(blob) < minLen || blob[0] != blobVersion {
		return nil, ErrMalformedBlob
	}
	kekVer := binary.BigEndian.Uint32(blob[1:5])
	kek, ok := k.keys[kekVer]
	if !ok {
		return nil, fmt.Errorf("%w: v%d", ErrUnknownKEK, kekVer)
	}
	off := 5
	wrapNonce := blob[off : off+gcmNonceLen]
	off += gcmNonceLen
	wrapped := blob[off : off+wrappedDEKLen]
	off += wrappedDEKLen
	secNonce := blob[off : off+gcmNonceLen]
	off += gcmNonceLen
	sealed := blob[off:]

	dek, err := open(kek, wrapNonce, wrapped)
	if err != nil {
		return nil, fmt.Errorf("secrets: unwrap DEK: %w", err)
	}
	pt, err := open(dek, secNonce, sealed)
	if err != nil {
		return nil, fmt.Errorf("secrets: open payload: %w", err)
	}
	return pt, nil
}

func seal(key, plaintext []byte) (nonce, sealed []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcmNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, plaintext, nil), nil
}

func open(key, nonce, sealed []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, sealed, nil)
}

// NewPassword mints a URI-safe random credential (base64url, 192 bits).
func NewPassword() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
