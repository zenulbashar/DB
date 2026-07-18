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
		CreatedAt: time.Now().UTC(),
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
