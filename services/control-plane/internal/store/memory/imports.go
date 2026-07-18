package memory

import (
	"context"
	"sort"
	"time"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/ids"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

func (s *Store) CreateImport(_ context.Context, p store.CreateImportParams) (*domain.Import, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pr, ok := s.projects[p.ProjectID]
	if !ok || pr.OrgID != p.OrgID || pr.State == domain.ProjectDeleting {
		return nil, store.ErrNotFound
	}
	target := p.TargetBranchID
	if target == nil {
		target = pr.DefaultBranchID
	} else if b, ok := s.branches[*target]; !ok || b.ProjectID != p.ProjectID || b.State == domain.StateDeleting {
		return nil, store.ErrNotFound
	}
	now := time.Now().UTC()
	im := domain.Import{
		ID: ids.New("imp"), ProjectID: p.ProjectID, OrgID: p.OrgID,
		TargetBranchID: target, SourceKind: p.SourceKind, Mode: p.Mode,
		State: domain.ImportPending, SourceSecretID: ids.New("sec"),
		CreatedAt: now, UpdatedAt: now,
	}
	s.imports[im.ID] = im
	return &im, nil
}

func (s *Store) GetImport(_ context.Context, orgID, importID string) (*domain.Import, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	im, ok := s.imports[importID]
	if !ok || im.OrgID != orgID {
		return nil, store.ErrNotFound
	}
	return &im, nil
}

func (s *Store) ListImports(_ context.Context, orgID, projectID string, pg store.Page) ([]domain.Import, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pr, ok := s.projects[projectID]
	if !ok || pr.OrgID != orgID || pr.State == domain.ProjectDeleting {
		return nil, "", store.ErrNotFound
	}
	var all []domain.Import
	for _, im := range s.imports {
		if im.ProjectID == projectID {
			all = append(all, im)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	return paginate(all, pg, func(im domain.Import) string { return im.ID })
}

func (s *Store) TransitionImport(_ context.Context, orgID, importID string, p store.TransitionImportParams) (*domain.Import, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	im, ok := s.imports[importID]
	if !ok || im.OrgID != orgID {
		return nil, store.ErrNotFound
	}
	if !domain.CanTransition(im.Mode, im.State, p.To) {
		return nil, store.ErrConflict
	}
	im.State = p.To
	if p.Report != nil {
		im.Report = p.Report
	}
	if p.Checkpoints != nil {
		if im.Checkpoints == nil {
			im.Checkpoints = map[string]any{}
		}
		for k, v := range p.Checkpoints {
			im.Checkpoints[k] = v
		}
	}
	im.Error = p.Error
	im.UpdatedAt = time.Now().UTC()
	s.imports[importID] = im
	return &im, nil
}
