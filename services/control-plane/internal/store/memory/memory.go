// Package memory is the in-memory Store used by unit tests. It mirrors the
// Postgres implementation's semantics (org scoping, last-owner rule, slug
// uniqueness, bootstrap-once) so handler tests exercise real behaviour.
package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zenulbashar/DB/services/control-plane/internal/domain"
	"github.com/zenulbashar/DB/services/control-plane/internal/ids"
	"github.com/zenulbashar/DB/services/control-plane/internal/slug"
	"github.com/zenulbashar/DB/services/control-plane/internal/store"
)

type memberKey struct{ orgID, userID string }
type idemKey struct{ orgID, route, key string }

type idemEntry struct {
	resp    store.IdempotentResponse
	expires time.Time
}

type Store struct {
	mu       sync.Mutex
	orgs     map[string]domain.Org
	users    map[string]domain.User
	members  map[memberKey]domain.Member
	keys     map[string]domain.APIKey
	keyHash  map[string]string // hash -> key id
	projects map[string]domain.Project
	audit    []domain.AuditEntry
	idem     map[idemKey]idemEntry
}

func New() *Store {
	return &Store{
		orgs:     map[string]domain.Org{},
		users:    map[string]domain.User{},
		members:  map[memberKey]domain.Member{},
		keys:     map[string]domain.APIKey{},
		keyHash:  map[string]string{},
		projects: map[string]domain.Project{},
		idem:     map[idemKey]idemEntry{},
	}
}

func (s *Store) Close() {}

// --- privileged ---

func (s *Store) Bootstrapped(_ context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.orgs) > 0, nil
}

func (s *Store) Bootstrap(_ context.Context, p store.BootstrapParams) (*store.BootstrapResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.orgs) > 0 {
		return nil, store.ErrAlreadyBoot
	}
	now := time.Now().UTC()
	org := domain.Org{ID: ids.New(ids.Org), Name: p.OrgName, Slug: slug.Make(p.OrgName), Plan: "free", CreatedAt: now}
	user := domain.User{ID: ids.New(ids.User), Email: strings.ToLower(p.Email), Name: p.Name, CreatedAt: now}
	key := domain.APIKey{
		ID: ids.New(ids.APIKey), OrgID: org.ID, Name: p.KeyName, Prefix: p.KeyPrefix,
		Scopes: p.Scopes, CreatedAt: now,
	}
	s.orgs[org.ID] = org
	s.users[user.ID] = user
	s.members[memberKey{org.ID, user.ID}] = domain.Member{OrgID: org.ID, User: user, Role: domain.RoleOwner, AddedAt: now}
	s.keys[key.ID] = key
	s.keyHash[p.KeyHash] = key.ID
	return &store.BootstrapResult{Org: org, User: user, APIKey: key}, nil
}

func (s *Store) FindAPIKeyByHash(_ context.Context, hash string) (*store.APIKeyAuth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.keyHash[hash]
	if !ok {
		return nil, store.ErrKeyInvalid
	}
	k := s.keys[id]
	if k.RevokedAt != nil || (k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now())) {
		return nil, store.ErrKeyInvalid
	}
	return &store.APIKeyAuth{Key: k}, nil
}

func (s *Store) TouchAPIKey(_ context.Context, keyID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.keys[keyID]; ok {
		k.LastUsedAt = &at
		s.keys[keyID] = k
	}
	return nil
}

// --- org-scoped ---

func (s *Store) GetOrg(_ context.Context, orgID string) (*domain.Org, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[orgID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &o, nil
}

func (s *Store) UpdateOrgName(_ context.Context, orgID, name string) (*domain.Org, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orgs[orgID]
	if !ok {
		return nil, store.ErrNotFound
	}
	o.Name = name
	s.orgs[orgID] = o
	return &o, nil
}

func (s *Store) ListMembers(_ context.Context, orgID string) ([]domain.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Member
	for k, m := range s.members {
		if k.orgID == orgID {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].User.ID < out[j].User.ID })
	return out, nil
}

func (s *Store) AddMember(_ context.Context, orgID, email string, name *string, role domain.OrgRole) (*domain.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[orgID]; !ok {
		return nil, store.ErrNotFound
	}
	email = strings.ToLower(email)
	var user *domain.User
	for _, u := range s.users {
		if u.Email == email {
			uu := u
			user = &uu
			break
		}
	}
	now := time.Now().UTC()
	if user == nil {
		u := domain.User{ID: ids.New(ids.User), Email: email, Name: name, CreatedAt: now}
		s.users[u.ID] = u
		user = &u
	}
	mk := memberKey{orgID, user.ID}
	if _, exists := s.members[mk]; exists {
		return nil, store.ErrConflict
	}
	m := domain.Member{OrgID: orgID, User: *user, Role: role, AddedAt: now}
	s.members[mk] = m
	return &m, nil
}

func (s *Store) countOwnersLocked(orgID string) int {
	n := 0
	for k, m := range s.members {
		if k.orgID == orgID && m.Role == domain.RoleOwner {
			n++
		}
	}
	return n
}

func (s *Store) UpdateMemberRole(_ context.Context, orgID, userID string, role domain.OrgRole) (*domain.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mk := memberKey{orgID, userID}
	m, ok := s.members[mk]
	if !ok {
		return nil, store.ErrNotFound
	}
	if m.Role == domain.RoleOwner && role != domain.RoleOwner && s.countOwnersLocked(orgID) == 1 {
		return nil, store.ErrLastOwner
	}
	m.Role = role
	s.members[mk] = m
	return &m, nil
}

func (s *Store) RemoveMember(_ context.Context, orgID, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mk := memberKey{orgID, userID}
	m, ok := s.members[mk]
	if !ok {
		return store.ErrNotFound
	}
	if m.Role == domain.RoleOwner && s.countOwnersLocked(orgID) == 1 {
		return store.ErrLastOwner
	}
	delete(s.members, mk)
	return nil
}

func (s *Store) CreateAPIKey(_ context.Context, p store.CreateAPIKeyParams) (*domain.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[p.OrgID]; !ok {
		return nil, store.ErrNotFound
	}
	k := domain.APIKey{
		ID: ids.New(ids.APIKey), OrgID: p.OrgID, Name: p.Name, Prefix: p.Prefix,
		Scopes: p.Scopes, ExpiresAt: p.ExpiresAt, CreatedAt: time.Now().UTC(),
	}
	s.keys[k.ID] = k
	s.keyHash[p.Hash] = k.ID
	return &k, nil
}

func (s *Store) ListAPIKeys(_ context.Context, orgID string) ([]domain.APIKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.APIKey
	for _, k := range s.keys {
		if k.OrgID == orgID {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (s *Store) RevokeAPIKey(_ context.Context, orgID, keyID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.keys[keyID]
	if !ok || k.OrgID != orgID {
		return store.ErrNotFound
	}
	if k.RevokedAt == nil {
		k.RevokedAt = &at
		s.keys[keyID] = k
	}
	return nil
}

func (s *Store) CreateProject(_ context.Context, p store.CreateProjectParams) (*domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.orgs[p.OrgID]; !ok {
		return nil, store.ErrNotFound
	}
	base := slug.Make(p.Name)
	var sl string
	for attempt := 0; ; attempt++ {
		sl = slug.WithSuffix(base, attempt)
		taken := false
		for _, pr := range s.projects {
			if pr.OrgID == p.OrgID && pr.Slug == sl && pr.State != domain.ProjectDeleting {
				taken = true
				break
			}
		}
		if !taken {
			break
		}
	}
	pr := domain.Project{
		ID: ids.New(ids.Project), OrgID: p.OrgID, Name: p.Name, Slug: sl,
		Region: p.Region, PGVersion: p.PGVersion, State: domain.ProjectPending,
		CreatedAt: time.Now().UTC(),
	}
	s.projects[pr.ID] = pr
	return &pr, nil
}

func (s *Store) GetProject(_ context.Context, orgID, projectID string) (*domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pr, ok := s.projects[projectID]
	if !ok || pr.OrgID != orgID || pr.State == domain.ProjectDeleting {
		return nil, store.ErrNotFound
	}
	return &pr, nil
}

func (s *Store) ListProjects(_ context.Context, orgID string, pg store.Page) ([]domain.Project, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []domain.Project
	for _, pr := range s.projects {
		if pr.OrgID == orgID && pr.State != domain.ProjectDeleting {
			all = append(all, pr)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	return paginate(all, pg, func(p domain.Project) string { return p.ID })
}

func (s *Store) UpdateProjectName(_ context.Context, orgID, projectID, name string) (*domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pr, ok := s.projects[projectID]
	if !ok || pr.OrgID != orgID || pr.State == domain.ProjectDeleting {
		return nil, store.ErrNotFound
	}
	pr.Name = name
	s.projects[projectID] = pr
	return &pr, nil
}

func (s *Store) SoftDeleteProject(_ context.Context, orgID, projectID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pr, ok := s.projects[projectID]
	if !ok || pr.OrgID != orgID || pr.State == domain.ProjectDeleting {
		return store.ErrNotFound
	}
	pr.State = domain.ProjectDeleting
	s.projects[projectID] = pr
	return nil
}

func (s *Store) AppendAudit(_ context.Context, e domain.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = append(s.audit, e)
	return nil
}

func (s *Store) ListAudit(_ context.Context, orgID string, pg store.Page) ([]domain.AuditEntry, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []domain.AuditEntry
	for _, e := range s.audit {
		if e.OrgID == orgID {
			all = append(all, e)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	return paginate(all, pg, func(e domain.AuditEntry) string { return e.ID })
}

func (s *Store) GetIdempotent(_ context.Context, orgID, route, key string) (*store.IdempotentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.idem[idemKey{orgID, route, key}]
	if !ok || time.Now().After(e.expires) {
		return nil, store.ErrNotFound
	}
	resp := e.resp
	return &resp, nil
}

func (s *Store) PutIdempotent(_ context.Context, orgID, route, key string, resp store.IdempotentResponse, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idem[idemKey{orgID, route, key}] = idemEntry{resp: resp, expires: expires}
	return nil
}

// paginate applies newest-first cursor pagination over an already-sorted
// (descending by ID) slice.
func paginate[T any](all []T, pg store.Page, id func(T) string) ([]T, string, error) {
	limit := pg.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	start := 0
	if pg.Cursor != "" {
		for i, item := range all {
			if id(item) < pg.Cursor {
				start = i
				break
			}
			start = len(all)
		}
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	page := all[start:end]
	next := ""
	if end < len(all) && len(page) > 0 {
		next = id(page[len(page)-1])
	}
	return page, next, nil
}
