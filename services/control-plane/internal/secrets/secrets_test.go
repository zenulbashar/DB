package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(k)
}

func TestRoundTrip(t *testing.T) {
	kr, err := ParseKeyring("1:"+testKey(t), "")
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("postgresql://app:hunter2@ep-x.syd1.db.nimbus.app/db")
	blob, ver, err := kr.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	if ver != 1 {
		t.Fatalf("version = %d", ver)
	}
	if bytes.Contains(blob, secret) || bytes.Contains(blob, []byte("hunter2")) {
		t.Fatal("plaintext leaked into blob")
	}
	got, err := kr.Decrypt(blob)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatal("roundtrip mismatch")
	}
}

func TestTamperDetection(t *testing.T) {
	kr, _ := ParseKeyring("1:"+testKey(t), "")
	blob, _, _ := kr.Encrypt([]byte("s3cret"))
	blob[len(blob)-1] ^= 0xff
	if _, err := kr.Decrypt(blob); err == nil {
		t.Fatal("tampered blob must not decrypt")
	}
}

func TestWrongKeyFails(t *testing.T) {
	a, _ := ParseKeyring("1:"+testKey(t), "")
	b, _ := ParseKeyring("1:"+testKey(t), "")
	blob, _, _ := a.Encrypt([]byte("s3cret"))
	if _, err := b.Decrypt(blob); err == nil {
		t.Fatal("foreign keyring must not decrypt")
	}
}

func TestRotationDecryptsOldVersions(t *testing.T) {
	k1 := testKey(t)
	old, _ := ParseKeyring("1:"+k1, "")
	blob, _, _ := old.Encrypt([]byte("legacy"))

	rotated, err := ParseKeyring("1:"+k1+",2:"+testKey(t), "2")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.ActiveVersion() != 2 {
		t.Fatalf("active = %d", rotated.ActiveVersion())
	}
	got, err := rotated.Decrypt(blob)
	if err != nil || string(got) != "legacy" {
		t.Fatalf("old blob must decrypt after rotation: %v", err)
	}
	// New encryptions use v2.
	blob2, ver, _ := rotated.Encrypt([]byte("fresh"))
	if ver != 2 {
		t.Fatalf("new encryption used v%d", ver)
	}
	if _, err := old.Decrypt(blob2); err == nil {
		t.Fatal("v2 blob must not decrypt with a v1-only keyring")
	}
}

func TestParseKeyringValidation(t *testing.T) {
	for _, bad := range []string{"", "1:short", "x:" + testKey(t), "0:" + testKey(t)} {
		if _, err := ParseKeyring(bad, ""); err == nil {
			t.Errorf("ParseKeyring(%q) must fail", bad)
		}
	}
	if _, err := ParseKeyring("1:"+testKey(t), "9"); err == nil {
		t.Error("active version outside keyring must fail")
	}
}

func TestNewPasswordShape(t *testing.T) {
	pw, err := NewPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) != 32 {
		t.Fatalf("len = %d, want 32", len(pw))
	}
	if strings.ContainsAny(pw, "+/=@:") {
		t.Fatalf("password %q must be URI-safe", pw)
	}
}
