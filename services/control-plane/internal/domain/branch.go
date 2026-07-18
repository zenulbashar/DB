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
	// StateSuspending and StateResuming are the transitional compute states the
	// reconciler converges for scale-to-zero (ADR-014): ready → suspending →
	// suspended and suspended → resuming → ready. The reconciler only ever acts
	// on transitional states, so these are how "please hibernate / please wake"
	// is expressed durably (a wake survives a reconciler restart).
	StateSuspending ResourceState = "suspending"
	StateSuspended  ResourceState = "suspended"
	StateResuming   ResourceState = "resuming"
	StateError      ResourceState = "error"
	StateDeleting   ResourceState = "deleting"
)

// resourceTransitions is the compute-lifecycle state machine shared by branches
// and endpoints for the suspend/wake path (ADR-014). It is intentionally scoped
// to the scale-to-zero edges; the provisioning/deleting lifecycle is enforced
// by the reconciler's own guarded writes and is not modelled here. Branches had
// no central transition validator before (unlike imports); this adds one as
// defence-in-depth over the store's guarded SQL predicates.
var resourceTransitions = map[ResourceState][]ResourceState{
	StateReady:      {StateSuspending},
	StateSuspending: {StateSuspended, StateError},
	StateSuspended:  {StateResuming},
	StateResuming:   {StateReady, StateError},
}

// CanTransitionResource reports whether from → to is a legal scale-to-zero
// step. Same-state (from == to) is reported as false — callers treat an
// idempotent no-op (e.g. resume an already-resuming branch) separately from a
// real transition so a connection storm coalesces instead of erroring.
func CanTransitionResource(from, to ResourceState) bool {
	for _, next := range resourceTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

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
