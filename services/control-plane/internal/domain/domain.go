// Package domain holds the control-plane resource model. Shapes mirror
// api/openapi.yaml — the spec is the contract, these are its Go projection.
package domain

import "time"

type OrgRole string

const (
	RoleOwner  OrgRole = "owner"
	RoleAdmin  OrgRole = "admin"
	RoleMember OrgRole = "member"
	RoleViewer OrgRole = "viewer"
)

func (r OrgRole) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleMember, RoleViewer:
		return true
	}
	return false
}

type Scope string

const (
	ScopeOrgsRead      Scope = "orgs:read"
	ScopeOrgsWrite     Scope = "orgs:write"
	ScopeMembersManage Scope = "members:manage"
	ScopeProjectsRead  Scope = "projects:read"
	ScopeProjectsWrite Scope = "projects:write"
	ScopeProjectsProv  Scope = "projects:provision"
	ScopeBranchesRead  Scope = "branches:read"
	ScopeBranchesWrite Scope = "branches:write"
	ScopeEndpointsRead Scope = "endpoints:read"
	ScopeRolesRead     Scope = "roles:read"
	ScopeRolesWrite    Scope = "roles:write"
	ScopeBackupsRead   Scope = "backups:read"
	ScopeBackupsWrite  Scope = "backups:write"
	ScopeRestoresWrite Scope = "restores:write"
	ScopeImportsRead   Scope = "imports:read"
	ScopeImportsWrite  Scope = "imports:write"
	ScopeMetricsRead   Scope = "metrics:read"
	ScopeAuditRead     Scope = "audit:read"
	ScopeKeysManage    Scope = "keys:manage"
	ScopeWebhooks      Scope = "webhooks:manage"
	ScopeUsageRead     Scope = "usage:read"
)

// AllScopes is the full grant used by the bootstrap owner key.
var AllScopes = []Scope{
	ScopeOrgsRead, ScopeOrgsWrite, ScopeMembersManage,
	ScopeProjectsRead, ScopeProjectsWrite, ScopeProjectsProv,
	ScopeBranchesRead, ScopeBranchesWrite, ScopeEndpointsRead,
	ScopeRolesRead, ScopeRolesWrite,
	ScopeBackupsRead, ScopeBackupsWrite, ScopeRestoresWrite,
	ScopeImportsRead, ScopeImportsWrite,
	ScopeMetricsRead, ScopeAuditRead, ScopeKeysManage, ScopeWebhooks, ScopeUsageRead,
}

func ValidScope(s Scope) bool {
	for _, v := range AllScopes {
		if v == s {
			return true
		}
	}
	return false
}

type Org struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Plan      string    `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
}

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      *string   `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Member struct {
	OrgID   string    `json:"org_id"`
	User    User      `json:"user"`
	Role    OrgRole   `json:"role"`
	AddedAt time.Time `json:"added_at"`
}

type APIKey struct {
	ID         string     `json:"id"`
	OrgID      string     `json:"org_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []Scope    `json:"scopes"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type ProjectState string

const (
	ProjectPending      ProjectState = "pending"
	ProjectProvisioning ProjectState = "provisioning"
	ProjectReady        ProjectState = "ready"
	ProjectError        ProjectState = "error"
	ProjectDeleting     ProjectState = "deleting"
)

type Project struct {
	ID        string       `json:"id"`
	OrgID     string       `json:"org_id"`
	Name      string       `json:"name"`
	Slug      string       `json:"slug"`
	Region    string       `json:"region"`
	PGVersion int          `json:"pg_version"`
	State     ProjectState `json:"state"`
	CreatedAt time.Time    `json:"created_at"`
}

type ActorType string

const (
	ActorAPIKey ActorType = "api_key"
	ActorUser   ActorType = "user"
	ActorSystem ActorType = "system"
)

type AuditEntry struct {
	ID         string         `json:"id"`
	OrgID      string         `json:"org_id"`
	ActorType  ActorType      `json:"actor_type"`
	ActorID    string         `json:"actor_id"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	IP         *string        `json:"ip"`
	At         time.Time      `json:"at"`
	Details    map[string]any `json:"details"`
}
