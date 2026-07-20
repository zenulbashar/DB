package memory

import (
	"context"
	"sort"
	"time"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

// Platform-operator reads (ADR-018), mirroring postgres/admin.go semantics.
// The memory store has no gateway-activity aggregation, so LastActiveAt stays
// nil here (the postgres store owns the real signal).

func branchRunning(state domain.ResourceState) bool {
	switch state {
	case domain.StateProvisioning, domain.StateReady, domain.StateSuspending,
		domain.StateResuming, domain.StateResizing:
		return true
	}
	return false
}

func importRunning(state domain.ImportState) bool {
	switch state {
	case domain.ImportCutOver, domain.ImportVerified, domain.ImportFailed, domain.ImportAborted:
		return false
	}
	return true
}

func (s *Store) AdminOverview(_ context.Context) (*domain.AdminOverview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := &domain.AdminOverview{
		Orgs:            len(s.orgs),
		Users:           len(s.users),
		BranchesByState: map[string]int{},
		ImportsByState:  map[string]int{},
	}
	for _, p := range s.projects {
		if p.State != domain.ProjectDeleting {
			o.Projects++
		}
	}
	for _, b := range s.branches {
		if b.State == domain.StateDeleting {
			continue
		}
		o.Branches++
		o.BranchesByState[string(b.State)]++
		if branchRunning(b.State) {
			o.AllocatedCU += b.Compute.EffectiveCU()
		}
	}
	for _, e := range s.endpoints {
		if e.State != domain.StateDeleting {
			o.Endpoints++
		}
	}
	for _, k := range s.keys {
		if k.RevokedAt == nil && (k.ExpiresAt == nil || k.ExpiresAt.After(time.Now().UTC())) {
			o.ActiveAPIKeys++
		}
	}
	for _, im := range s.imports {
		o.ImportsByState[string(im.State)]++
	}
	return o, nil
}

func (s *Store) AdminListOrgUsage(_ context.Context) ([]domain.OrgUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.OrgUsage
	for _, org := range s.orgs {
		u := domain.OrgUsage{Org: org, BranchesByState: map[string]int{}}
		for mk := range s.members {
			if mk.orgID == org.ID {
				u.Members++
			}
		}
		for _, p := range s.projects {
			if p.OrgID == org.ID && p.State != domain.ProjectDeleting {
				u.Projects++
			}
		}
		for _, b := range s.branches {
			if b.OrgID != org.ID || b.State == domain.StateDeleting {
				continue
			}
			u.Branches++
			u.BranchesByState[string(b.State)]++
			if branchRunning(b.State) {
				u.AllocatedCU += b.Compute.EffectiveCU()
			}
		}
		for _, e := range s.endpoints {
			if e.OrgID == org.ID && e.State != domain.StateDeleting {
				u.Endpoints++
			}
		}
		for _, k := range s.keys {
			if k.OrgID == org.ID && k.RevokedAt == nil &&
				(k.ExpiresAt == nil || k.ExpiresAt.After(time.Now().UTC())) {
				u.ActiveAPIKeys++
			}
		}
		for _, im := range s.imports {
			if im.OrgID == org.ID && importRunning(im.State) {
				u.ImportsRunning++
			}
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Org.CreatedAt.Equal(out[j].Org.CreatedAt) {
			return out[i].Org.CreatedAt.Before(out[j].Org.CreatedAt)
		}
		return out[i].Org.ID < out[j].Org.ID
	})
	return out, nil
}

func (s *Store) AdminListBranches(_ context.Context, state string, limit int) ([]domain.AdminBranch, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.AdminBranch
	for _, b := range s.branches {
		if b.State == domain.StateDeleting || (state != "" && string(b.State) != state) {
			continue
		}
		ab := domain.AdminBranch{Branch: b, OrgID: b.OrgID}
		if org, ok := s.orgs[b.OrgID]; ok {
			ab.OrgName = org.Name
		}
		if p, ok := s.projects[b.ProjectID]; ok {
			ab.ProjectName = p.Name
		}
		ab.Endpoints = nil // list responses omit endpoints, as in tenant lists
		out = append(out, ab)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) AdminRecentAudit(_ context.Context, limit int) ([]domain.AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.AuditEntry, 0, limit)
	for i := len(s.audit) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.audit[i])
	}
	return out, nil
}

func (s *Store) ResolveBranchOrg(_ context.Context, branchID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.branches[branchID]
	if !ok || b.State == domain.StateDeleting {
		return "", store.ErrNotFound
	}
	return b.OrgID, nil
}
