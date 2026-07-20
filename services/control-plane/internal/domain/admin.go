package domain

import "time"

// Platform-operator (admin) read models (ADR-018). These are privileged,
// cross-tenant aggregates — they exist only behind the NDB_ADMIN_TOKEN surface
// and must never be reachable with a tenant credential.

// AdminOverview is the whole-platform snapshot the operator lands on.
type AdminOverview struct {
	Orgs            int            `json:"orgs"`
	Users           int            `json:"users"`
	Projects        int            `json:"projects"`
	Branches        int            `json:"branches"`
	BranchesByState map[string]int `json:"branches_by_state"`
	Endpoints       int            `json:"endpoints"`
	ImportsByState  map[string]int `json:"imports_by_state"`
	ActiveAPIKeys   int            `json:"active_api_keys"`
	// AllocatedCU sums EffectiveCU over live, non-suspended branches — the
	// platform's current compute footprint (v1 "usage"; metered usage is the
	// Phase 7 pipeline).
	AllocatedCU float64 `json:"allocated_cu"`
}

// OrgUsage is one tenant's resource inventory and activity recency.
type OrgUsage struct {
	Org             Org            `json:"org"`
	Members         int            `json:"members"`
	Projects        int            `json:"projects"`
	Branches        int            `json:"branches"`
	BranchesByState map[string]int `json:"branches_by_state"`
	Endpoints       int            `json:"endpoints"`
	ActiveAPIKeys   int            `json:"active_api_keys"`
	ImportsRunning  int            `json:"imports_running"`
	AllocatedCU     float64        `json:"allocated_cu"`
	// LastActiveAt is the most recent gateway-observed activity across the
	// org's branches (nil when nothing has ever been observed).
	LastActiveAt *time.Time `json:"last_active_at"`
}

// AdminBranch is a branch with the tenant context an operator needs to act.
type AdminBranch struct {
	Branch
	OrgID       string `json:"org_id"`
	OrgName     string `json:"org_name"`
	ProjectName string `json:"project_name"`
}
