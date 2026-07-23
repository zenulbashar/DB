package domain

import "testing"

// NDB_DOMAIN (ADR-020): the endpoint-host suffix is configurable for
// self-hosting, defaulting to db.zaleit.com.au so nothing else changes.
func TestEndpointHostConfigurableDomain(t *testing.T) {
	if got := EndpointHost("ep_01abc", "syd1"); got != "ep-01abc.syd1.db.zaleit.com.au" {
		t.Fatalf("default domain host = %q", got)
	}

	SetBaseDomain("db.example.dev")
	defer SetBaseDomain("db.zaleit.com.au")

	if got := EndpointHost("ep_01abc", "syd1"); got != "ep-01abc.syd1.db.example.dev" {
		t.Fatalf("custom domain host = %q", got)
	}
	if BaseDomain() != "db.example.dev" {
		t.Fatalf("BaseDomain = %q", BaseDomain())
	}

	// Empty never wipes the configured domain (defensive boot behavior).
	SetBaseDomain("")
	if BaseDomain() != "db.example.dev" {
		t.Fatalf("empty SetBaseDomain must be a no-op, got %q", BaseDomain())
	}
}
