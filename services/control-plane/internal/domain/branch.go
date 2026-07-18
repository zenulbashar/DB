package domain

import "time"

type BranchRole string

const (
	BranchProduction  BranchRole = "production"
	BranchPreview     BranchRole = "preview"
	BranchDevelopment BranchRole = "development"
)

func (r BranchRole) Valid() bool {
	switch r {
	case BranchProduction, BranchPreview, BranchDevelopment:
		return true
	}
	return false
}

// ResourceState is shared by branches and endpoints (and later clusters);
// the console's status system renders it uniformly (DESIGN_SYSTEM_MAPPING §2).
type ResourceState string

const (
	StateProvisioning ResourceState = "provisioning"
	StateReady        ResourceState = "ready"
	StateSuspended    ResourceState = "suspended"
	StateError        ResourceState = "error"
	StateDeleting     ResourceState = "deleting"
)

type Compute struct {
	MinCU           float64 `json:"min_cu"`
	MaxCU           float64 `json:"max_cu"`
	SuspendTimeoutS int     `json:"suspend_timeout_s"`
}

type Branch struct {
	ID            string        `json:"id"`
	ProjectID     string        `json:"project_id"`
	OrgID         string        `json:"-"` // internal; API responses are already org-scoped
	ParentID      *string       `json:"parent"`
	Name          string        `json:"name"`
	Role          BranchRole    `json:"role"`
	State         ResourceState `json:"state"`
	Compute       Compute       `json:"compute"`
	RetentionDays int           `json:"retention_days"`
	CreatedAt     time.Time     `json:"created_at"`
	// Populated on single-branch reads; nil on list responses.
	Endpoints []Endpoint `json:"endpoints,omitempty"`
}

type EndpointKind string

const (
	EndpointRWDirect EndpointKind = "rw_direct"
	EndpointRWPooled EndpointKind = "rw_pooled"
	EndpointROPooled EndpointKind = "ro_pooled"
)

type DBRole struct {
	ID        string    `json:"id"`
	BranchID  string    `json:"branch_id"`
	OrgID     string    `json:"-"`
	Name      string    `json:"name"`
	SecretID  string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

type Database struct {
	ID          string    `json:"id"`
	BranchID    string    `json:"branch_id"`
	OrgID       string    `json:"-"`
	Name        string    `json:"name"`
	OwnerRoleID string    `json:"owner_role_id"`
	CreatedAt   time.Time `json:"created_at"`
}

type Endpoint struct {
	ID        string        `json:"id"`
	BranchID  string        `json:"branch_id"`
	OrgID     string        `json:"-"`
	Kind      EndpointKind  `json:"kind"`
	Host      string        `json:"host"`
	State     ResourceState `json:"state"`
	CreatedAt time.Time     `json:"created_at"`
}
