package memory

import (
	"context"
	"sort"
	"time"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/ids"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

// createBranchLocked mirrors postgres.createBranchTx; callers hold s.mu.
func (s *Store) createBranchLocked(p store.CreateBranchParams) (*domain.Branch, error) {
	for _, b := range s.branches {
		if b.ProjectID == p.ProjectID && b.Name == p.Name && b.State != domain.StateDeleting {
			return nil, store.ErrConflict
		}
	}
	c := p.Compute
	if c.MinCU == 0 {
		c.MinCU = 0.25
	}
	if c.MaxCU == 0 {
		c.MaxCU = 2
	}
	if c.SuspendTimeoutS == 0 {
		c.SuspendTimeoutS = 300
	}
	b := domain.Branch{
		ID: ids.New(ids.Branch), ProjectID: p.ProjectID, OrgID: p.OrgID,
		ParentID: p.ParentID, Name: p.Name, Role: p.Role,
		State: domain.StateProvisioning, Compute: c, RetentionDays: 7,
		BootstrapAt: p.BootstrapAt, CreatedAt: time.Now().UTC(),
	}
	for _, kind := range []domain.EndpointKind{domain.EndpointRWDirect, domain.EndpointRWPooled} {
		ep := domain.Endpoint{
			ID: ids.New(ids.Endpoint), BranchID: b.ID, OrgID: p.OrgID, Kind: kind,
			State: domain.StateProvisioning, CreatedAt: b.CreatedAt,
		}
		ep.Host = domain.EndpointHost(ep.ID, p.Region)
		s.endpoints[ep.ID] = ep
		b.Endpoints = append(b.Endpoints, ep)
	}
	stored := b
	stored.Endpoints = nil
	s.branches[b.ID] = stored
	return &b, nil
}

func (s *Store) CreateBranch(_ context.Context, p store.CreateBranchParams) (*domain.Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pr, ok := s.projects[p.ProjectID]
	if !ok || pr.OrgID != p.OrgID || pr.State == domain.ProjectDeleting {
		return nil, store.ErrNotFound
	}
	p.Region = pr.Region
	if p.ParentID == nil {
		p.ParentID = pr.DefaultBranchID
	} else if b, ok := s.branches[*p.ParentID]; !ok || b.ProjectID != p.ProjectID || b.State == domain.StateDeleting {
		return nil, store.ErrNotFound
	}
	return s.createBranchLocked(p)
}

func (s *Store) GetBranch(_ context.Context, orgID, branchID string) (*domain.Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.branches[branchID]
	if !ok || b.OrgID != orgID || b.State == domain.StateDeleting {
		return nil, store.ErrNotFound
	}
	b.Endpoints = s.endpointsForLocked(branchID)
	return &b, nil
}

func (s *Store) ListBranches(_ context.Context, orgID, projectID string, pg store.Page) ([]domain.Branch, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pr, ok := s.projects[projectID]
	if !ok || pr.OrgID != orgID || pr.State == domain.ProjectDeleting {
		return nil, "", store.ErrNotFound
	}
	var all []domain.Branch
	for _, b := range s.branches {
		if b.ProjectID == projectID && b.State != domain.StateDeleting {
			all = append(all, b)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	return paginate(all, pg, func(b domain.Branch) string { return b.ID })
}

func (s *Store) UpdateBranch(_ context.Context, orgID, branchID string, p store.UpdateBranchParams) (*domain.Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.branches[branchID]
	if !ok || b.OrgID != orgID || b.State == domain.StateDeleting {
		return nil, store.ErrNotFound
	}
	if p.Name != nil {
		for _, other := range s.branches {
			if other.ProjectID == b.ProjectID && other.ID != b.ID &&
				other.Name == *p.Name && other.State != domain.StateDeleting {
				return nil, store.ErrConflict
			}
		}
		b.Name = *p.Name
	}
	if p.MinCU != nil {
		b.Compute.MinCU = *p.MinCU
	}
	if p.MaxCU != nil {
		b.Compute.MaxCU = *p.MaxCU
	}
	if p.SuspendTimeoutS != nil {
		b.Compute.SuspendTimeoutS = *p.SuspendTimeoutS
	}
	if p.RetentionDays != nil {
		b.RetentionDays = *p.RetentionDays
	}
	s.branches[branchID] = b
	return &b, nil
}

func (s *Store) SoftDeleteBranch(_ context.Context, orgID, branchID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.branches[branchID]
	if !ok || b.OrgID != orgID || b.State == domain.StateDeleting {
		return store.ErrNotFound
	}
	if pr, ok := s.projects[b.ProjectID]; ok && pr.DefaultBranchID != nil && *pr.DefaultBranchID == branchID {
		return store.ErrDefaultBranch
	}
	b.State = domain.StateDeleting
	s.branches[branchID] = b
	for id, ep := range s.endpoints {
		if ep.BranchID == branchID {
			ep.State = domain.StateDeleting
			s.endpoints[id] = ep
		}
	}
	return nil
}

func (s *Store) SuspendBranch(_ context.Context, orgID, branchID string) (*domain.Branch, error) {
	return s.transitionBranchState(orgID, branchID,
		domain.StateReady, domain.StateSuspending,
		domain.StateSuspending, domain.StateSuspended)
}

func (s *Store) ResumeBranch(_ context.Context, orgID, branchID string) (*domain.Branch, error) {
	return s.transitionBranchState(orgID, branchID,
		domain.StateSuspended, domain.StateResuming,
		domain.StateResuming, domain.StateReady)
}

// ReportGatewayActivity records a gateway's per-branch counts (ADR-015). The
// memory store keeps only the last report per gateway (the postgres store owns
// the real cross-gateway aggregation + idle sweep); this satisfies the Store
// interface for handler tests.
func (s *Store) ReportGatewayActivity(_ context.Context, gatewayID string, counts map[string]int) error {
	if gatewayID == "" || len(counts) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]int{}
	for k, v := range counts {
		m[k] = v
	}
	s.gwActivity[gatewayID] = m
	return nil
}

// WakeBranchByID resolves the branch's org, then reuses ResumeBranch (mirrors
// the postgres store; the privileged cross-tenant wake path — ADR-014).
func (s *Store) WakeBranchByID(ctx context.Context, branchID string) (*domain.Branch, error) {
	s.mu.Lock()
	b, ok := s.branches[branchID]
	if !ok || b.State == domain.StateDeleting {
		s.mu.Unlock()
		return nil, store.ErrNotFound
	}
	orgID := b.OrgID
	s.mu.Unlock()
	return s.ResumeBranch(ctx, orgID, branchID)
}

// transitionBranchState mirrors postgres.transitionBranchState: a guarded,
// idempotent compute-state flip on the branch and its endpoints in lockstep.
func (s *Store) transitionBranchState(orgID, branchID string,
	from, to domain.ResourceState, noopStates ...domain.ResourceState) (*domain.Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !domain.CanTransitionResource(from, to) {
		return nil, store.ErrConflict
	}
	b, ok := s.branches[branchID]
	if !ok || b.OrgID != orgID || b.State == domain.StateDeleting {
		return nil, store.ErrNotFound
	}
	if b.State != from {
		for _, noop := range noopStates {
			if b.State == noop {
				b.Endpoints = s.endpointsForLocked(branchID)
				return &b, nil
			}
		}
		return nil, store.ErrConflict
	}
	b.State = to
	s.branches[branchID] = b
	for id, ep := range s.endpoints {
		if ep.BranchID == branchID && ep.State == from {
			ep.State = to
			s.endpoints[id] = ep
		}
	}
	b.Endpoints = s.endpointsForLocked(branchID)
	return &b, nil
}

func (s *Store) ResizeBranch(_ context.Context, orgID, branchID string, targetCU float64) (*domain.Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.branches[branchID]
	if !ok || b.OrgID != orgID || b.State == domain.StateDeleting {
		return nil, store.ErrNotFound
	}
	target := targetCU
	if target < b.Compute.MinCU {
		target = b.Compute.MinCU
	}
	if target > b.Compute.MaxCU {
		target = b.Compute.MaxCU
	}
	if b.State != domain.StateReady && b.State != domain.StateResizing {
		return nil, store.ErrConflict
	}
	cur := b.Compute.EffectiveCU()
	if b.State == domain.StateReady && cur == target {
		b.Endpoints = s.endpointsForLocked(branchID)
		return &b, nil
	}
	b.State = domain.StateResizing
	b.Compute.CurrentCU = target
	s.branches[branchID] = b
	b.Endpoints = s.endpointsForLocked(branchID)
	return &b, nil
}

func (s *Store) CreateEndpoint(_ context.Context, orgID, branchID string, kind domain.EndpointKind) (*domain.Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.branches[branchID]
	if !ok || b.OrgID != orgID || b.State == domain.StateDeleting {
		return nil, store.ErrNotFound
	}
	for _, ep := range s.endpoints {
		if ep.BranchID == branchID && ep.Kind == kind && ep.State != domain.StateDeleting {
			return nil, store.ErrConflict
		}
	}
	pr := s.projects[b.ProjectID]
	ep := domain.Endpoint{
		ID: ids.New(ids.Endpoint), BranchID: branchID, OrgID: orgID, Kind: kind,
		State: domain.StateProvisioning, CreatedAt: time.Now().UTC(),
	}
	ep.Host = domain.EndpointHost(ep.ID, pr.Region)
	s.endpoints[ep.ID] = ep
	return &ep, nil
}

func (s *Store) ListEndpoints(_ context.Context, orgID, branchID string) ([]domain.Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.branches[branchID]
	if !ok || b.OrgID != orgID || b.State == domain.StateDeleting {
		return nil, store.ErrNotFound
	}
	return s.endpointsForLocked(branchID), nil
}

func (s *Store) endpointsForLocked(branchID string) []domain.Endpoint {
	var out []domain.Endpoint
	for _, ep := range s.endpoints {
		if ep.BranchID == branchID {
			out = append(out, ep)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}
