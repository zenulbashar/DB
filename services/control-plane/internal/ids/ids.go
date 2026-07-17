// Package ids issues prefixed, lexicographically sortable identifiers
// (ULIDs) for every API-visible resource, per API_SPECIFICATION.md §1.
package ids

import (
	"crypto/rand"
	"strings"

	"github.com/oklog/ulid/v2"
)

const (
	Org     = "org"
	User    = "usr"
	APIKey  = "key"
	Project = "prj"
	Branch  = "br"
	Audit   = "aud"
	Request = "req"
)

// New returns "<prefix>_<ulid>", e.g. "org_01JZX7Y2MB...".
func New(prefix string) string {
	return prefix + "_" + strings.ToLower(ulid.MustNew(ulid.Now(), rand.Reader).String())
}

// HasPrefix reports whether id carries the given resource prefix.
func HasPrefix(id, prefix string) bool {
	return strings.HasPrefix(id, prefix+"_")
}
