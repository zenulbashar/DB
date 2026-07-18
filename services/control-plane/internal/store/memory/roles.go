package memory

import (
	"context"
	"sort"
	"time"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/ids"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

type roleKey struct{ branchID, name string }
type dbKey struct{ branchID, name string }

func (s *Store) requireBranchLocked(orgID, branchID string) error {
	b, ok := s.branches[branchID]
	if !ok || b.OrgID != orgID || b.State == domain.StateDeleting {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) createDBRoleLocked(p store.CreateDBRoleParams) (*domain.DBRole, error) {
	rk := roleKey{p.BranchID, p.Name}
	if _, exists := s.roles[rk]; exists {
		return nil, store.ErrConflict
	}
	r := domain.DBRole{
		ID: ids.New("role"), BranchID: p.BranchID, OrgID: p.OrgID,
		Name: p.Name, SecretID: ids.New("sec"), CreatedAt: time.Now().UTC(),
	}
	s.roles[rk] = r
	s.roleSecrets[rk] = p.Secret.Ciphertext
	return &r, nil
}

func (s *Store) CreateDBRole(_ context.Context, p store.CreateDBRoleParams) (*domain.DBRole, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireBranchLocked(p.OrgID, p.BranchID); err != nil {
		return nil, err
	}
	return s.createDBRoleLocked(p)
}

func (s *Store) ListDBRoles(_ context.Context, orgID, branchID string) ([]domain.DBRole, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireBranchLocked(orgID, branchID); err != nil {
		return nil, err
	}
	var out []domain.DBRole
	for k, r := range s.roles {
		if k.branchID == branchID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) DeleteDBRole(_ context.Context, orgID, branchID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := roleKey{branchID, name}
	r, ok := s.roles[rk]
	if !ok || r.OrgID != orgID {
		return store.ErrNotFound
	}
	for _, d := range s.databases {
		if d.OwnerRoleID == r.ID {
			return store.ErrConflict
		}
	}
	delete(s.roles, rk)
	delete(s.roleSecrets, rk)
	return nil
}

func (s *Store) ResetDBRolePassword(_ context.Context, orgID, branchID, name string, secret store.SecretMaterial) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := roleKey{branchID, name}
	r, ok := s.roles[rk]
	if !ok || r.OrgID != orgID {
		return store.ErrNotFound
	}
	s.roleSecrets[rk] = secret.Ciphertext
	return nil
}

func (s *Store) GetDBRoleSecret(_ context.Context, orgID, branchID, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := roleKey{branchID, name}
	r, ok := s.roles[rk]
	if !ok || r.OrgID != orgID {
		return nil, store.ErrNotFound
	}
	return s.roleSecrets[rk], nil
}

func (s *Store) createDatabaseLocked(orgID, branchID, name, ownerRoleName string) (*domain.Database, error) {
	role, ok := s.roles[roleKey{branchID, ownerRoleName}]
	if !ok {
		return nil, store.ErrNotFound
	}
	dk := dbKey{branchID, name}
	if _, exists := s.databases[dk]; exists {
		return nil, store.ErrConflict
	}
	d := domain.Database{
		ID: ids.New("db"), BranchID: branchID, OrgID: orgID,
		Name: name, OwnerRoleID: role.ID, CreatedAt: time.Now().UTC(),
	}
	s.databases[dk] = d
	return &d, nil
}

func (s *Store) CreateDatabase(_ context.Context, orgID, branchID, name, ownerRoleName string) (*domain.Database, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireBranchLocked(orgID, branchID); err != nil {
		return nil, err
	}
	return s.createDatabaseLocked(orgID, branchID, name, ownerRoleName)
}

func (s *Store) ListDatabases(_ context.Context, orgID, branchID string) ([]domain.Database, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireBranchLocked(orgID, branchID); err != nil {
		return nil, err
	}
	var out []domain.Database
	for k, d := range s.databases {
		if k.branchID == branchID {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) DeleteDatabase(_ context.Context, orgID, branchID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dk := dbKey{branchID, name}
	d, ok := s.databases[dk]
	if !ok || d.OrgID != orgID {
		return store.ErrNotFound
	}
	delete(s.databases, dk)
	return nil
}
