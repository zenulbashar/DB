package dumprestore

import "testing"

func TestSplitPassword(t *testing.T) {
	cases := []struct {
		in       string
		wantURL  string
		wantPass string
	}{
		{"postgres://user:secret@host:5432/db?sslmode=require",
			"postgres://user@host:5432/db?sslmode=require", "secret"},
		{"postgres://user@host/db", "postgres://user@host/db", ""},
		{"postgres://host/db", "postgres://host/db", ""},
		// A password with URL-special characters must not leak into argv and
		// must round-trip out of the URL intact.
		{"postgres://u:p%40ss%20word@host/db", "postgres://u@host/db", "p@ss word"},
	}
	for _, tc := range cases {
		gotURL, gotPass, err := splitPassword(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if gotURL != tc.wantURL {
			t.Errorf("%s: url = %q, want %q", tc.in, gotURL, tc.wantURL)
		}
		if gotPass != tc.wantPass {
			t.Errorf("%s: pass = %q, want %q", tc.in, gotPass, tc.wantPass)
		}
		// The stripped URL must never still carry the password.
		if tc.wantPass != "" && contains(gotURL, tc.wantPass) {
			t.Errorf("%s: password leaked into stripped url %q", tc.in, gotURL)
		}
	}
}

func TestSplitPasswordRejectsGarbageWithoutLeaking(t *testing.T) {
	_, _, err := splitPassword("://:::bad")
	if err == nil {
		return // some inputs parse; fine
	}
	if contains(err.Error(), "bad") {
		t.Fatalf("parse error must not echo the input url: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
