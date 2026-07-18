package importworker

import (
	"strings"
	"testing"
)

func TestUrlToConnInfoQuotesSpecialValues(t *testing.T) {
	// A password containing a space and a quote must be single-quote wrapped
	// and escaped, not left to break the conninfo or inject a keyword.
	info, err := urlToConnInfo(`postgres://u:p%20a%27ss@h.example:6000/db?sslmode=require`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info, `host=h.example`) || !strings.Contains(info, `port=6000`) ||
		!strings.Contains(info, `dbname=db`) || !strings.Contains(info, `user=u`) ||
		!strings.Contains(info, `sslmode=require`) {
		t.Fatalf("missing expected fields: %q", info)
	}
	if !strings.Contains(info, `password='p a\'ss'`) {
		t.Fatalf("password not quote/escaped: %q", info)
	}
}

func TestUrlToConnInfoSimpleUnquoted(t *testing.T) {
	info, err := urlToConnInfo(`postgres://app:hunter2@db:5432/orders`)
	if err != nil {
		t.Fatal(err)
	}
	// Simple values stay unquoted.
	if !strings.Contains(info, `password=hunter2`) || strings.Contains(info, `'hunter2'`) {
		t.Fatalf("simple password should be unquoted: %q", info)
	}
}

func TestUrlToConnInfoParseErrorHidesURL(t *testing.T) {
	_, err := urlToConnInfo("postgres://u:s3cr3t@:%zz")
	if err == nil {
		t.Skip("input parsed; nothing to assert")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("parse error leaked the credential: %v", err)
	}
}

func TestQuoteConnInfo(t *testing.T) {
	cases := map[string]string{
		"simple":   "simple",
		"":         "''",
		"a b":      "'a b'",
		`a'b`:      `'a\'b'`,
		`a\b`:      `'a\\b'`,
		"tab\ttab": "'tab\ttab'",
	}
	for in, want := range cases {
		if got := quoteConnInfo(in); got != want {
			t.Errorf("quoteConnInfo(%q) = %q, want %q", in, got, want)
		}
	}
}
