// github.go — GitHub repo links + user map (issue #167).
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// GitHubLink binds a project to a GitHub repository.
type GitHubLink struct {
	ProjectID    string     `json:"project_id"`
	Owner        string     `json:"owner"`
	Repo         string     `json:"repo"`
	SyncMode     string     `json:"sync_mode"`
	ConnectedBy  string     `json:"connected_by,omitempty"`
	ConnectedAt  time.Time  `json:"connected_at"`
	LastImportAt *time.Time `json:"last_import_at,omitempty"`
	// AccessToken is never returned in JSON (json:"-").
	AccessToken string `json:"-"`
}

// GitHubUserMap maps a GitHub login to a Central Memory user.
type GitHubUserMap struct {
	GitHubLogin string    `json:"github_login"`
	GitHubID    int64     `json:"github_id,omitempty"`
	UserID      string    `json:"user_id,omitempty"`
	Email       string    `json:"email,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// GitHubStore is the persistence surface for Phase 7.
type GitHubStore interface {
	UpsertGitHubLink(ctx context.Context, link *GitHubLink) error
	GetGitHubLink(ctx context.Context, projectID string) (*GitHubLink, error)
	DeleteGitHubLink(ctx context.Context, projectID string) error
	TouchGitHubImport(ctx context.Context, projectID string) error
	UpsertGitHubUserMap(ctx context.Context, m *GitHubUserMap) error
	GetGitHubUserMap(ctx context.Context, login string) (*GitHubUserMap, error)
}

var _ GitHubStore = (*MemStore)(nil)
var _ GitHubStore = (*PostgresStore)(nil)

// ---- MemStore ----

func (s *MemStore) ensureGitHub() {
	if s.githubLinks == nil {
		s.githubLinks = make(map[string]*GitHubLink)
	}
	if s.githubUsers == nil {
		s.githubUsers = make(map[string]*GitHubUserMap)
	}
}

// UpsertGitHubLink stores or replaces the project↔repo link.
func (s *MemStore) UpsertGitHubLink(ctx context.Context, link *GitHubLink) error {
	if link == nil || strings.TrimSpace(link.ProjectID) == "" {
		return errors.New("store: github link project_id is required")
	}
	if strings.TrimSpace(link.Owner) == "" || strings.TrimSpace(link.Repo) == "" {
		return errors.New("store: github owner and repo are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureGitHub()
	cp := *link
	cp.Owner = strings.TrimSpace(link.Owner)
	cp.Repo = strings.TrimSpace(link.Repo)
	if cp.SyncMode == "" {
		cp.SyncMode = "manual"
	}
	if cp.ConnectedAt.IsZero() {
		cp.ConnectedAt = time.Now().UTC()
	}
	s.githubLinks[cp.ProjectID] = &cp
	return nil
}

// GetGitHubLink returns the link for a project or ErrNotFound.
func (s *MemStore) GetGitHubLink(ctx context.Context, projectID string) (*GitHubLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.ensureGitHub()
	link, ok := s.githubLinks[projectID]
	if !ok || link == nil {
		return nil, fmt.Errorf("store: github link %s: %w", projectID, ErrNotFound)
	}
	cp := *link
	return &cp, nil
}

// DeleteGitHubLink removes the link.
func (s *MemStore) DeleteGitHubLink(ctx context.Context, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureGitHub()
	if _, ok := s.githubLinks[projectID]; !ok {
		return fmt.Errorf("store: github link %s: %w", projectID, ErrNotFound)
	}
	delete(s.githubLinks, projectID)
	return nil
}

// TouchGitHubImport sets last_import_at to now.
func (s *MemStore) TouchGitHubImport(ctx context.Context, projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureGitHub()
	link, ok := s.githubLinks[projectID]
	if !ok || link == nil {
		return fmt.Errorf("store: github link %s: %w", projectID, ErrNotFound)
	}
	now := time.Now().UTC()
	link.LastImportAt = &now
	return nil
}

// UpsertGitHubUserMap stores a login→user mapping.
func (s *MemStore) UpsertGitHubUserMap(ctx context.Context, m *GitHubUserMap) error {
	if m == nil || strings.TrimSpace(m.GitHubLogin) == "" {
		return errors.New("store: github_login is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureGitHub()
	cp := *m
	cp.GitHubLogin = strings.ToLower(strings.TrimSpace(m.GitHubLogin))
	cp.UpdatedAt = time.Now().UTC()
	s.githubUsers[cp.GitHubLogin] = &cp
	return nil
}

// GetGitHubUserMap looks up a mapping by login.
func (s *MemStore) GetGitHubUserMap(ctx context.Context, login string) (*GitHubUserMap, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.ensureGitHub()
	m, ok := s.githubUsers[login]
	if !ok || m == nil {
		return nil, fmt.Errorf("store: github user %q: %w", login, ErrNotFound)
	}
	cp := *m
	return &cp, nil
}

// ---- PostgresStore ----

const githubLinkColumns = `project_id::TEXT, owner, repo, COALESCE(access_token, ''), sync_mode, ` +
	`COALESCE(connected_by::TEXT, ''), connected_at, last_import_at`

func scanGitHubLink(row pgx.Row) (*GitHubLink, error) {
	var link GitHubLink
	if err := row.Scan(
		&link.ProjectID, &link.Owner, &link.Repo, &link.AccessToken, &link.SyncMode,
		&link.ConnectedBy, &link.ConnectedAt, &link.LastImportAt,
	); err != nil {
		return nil, err
	}
	return &link, nil
}

func (s *PostgresStore) UpsertGitHubLink(ctx context.Context, link *GitHubLink) error {
	if link == nil || strings.TrimSpace(link.ProjectID) == "" {
		return errors.New("store: github link project_id is required")
	}
	owner := strings.TrimSpace(link.Owner)
	repo := strings.TrimSpace(link.Repo)
	if owner == "" || repo == "" {
		return errors.New("store: github owner and repo are required")
	}
	syncMode := strings.TrimSpace(link.SyncMode)
	if syncMode == "" {
		syncMode = "manual"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO github_links (project_id, owner, repo, access_token, sync_mode, connected_by)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')::UUID)
		ON CONFLICT (project_id) DO UPDATE SET
		  owner = EXCLUDED.owner,
		  repo = EXCLUDED.repo,
		  access_token = COALESCE(NULLIF(EXCLUDED.access_token, ''), github_links.access_token),
		  sync_mode = EXCLUDED.sync_mode,
		  connected_by = COALESCE(EXCLUDED.connected_by, github_links.connected_by),
		  connected_at = now()`,
		link.ProjectID, owner, repo, nullText(strings.TrimSpace(link.AccessToken)), syncMode, strings.TrimSpace(link.ConnectedBy))
	if err != nil {
		return fmt.Errorf("store: upsert github link: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetGitHubLink(ctx context.Context, projectID string) (*GitHubLink, error) {
	link, err := scanGitHubLink(s.pool.QueryRow(ctx,
		`SELECT `+githubLinkColumns+` FROM github_links WHERE project_id = $1`, projectID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: github link %s: %w", projectID, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get github link: %w", err)
	}
	return link, nil
}

func (s *PostgresStore) DeleteGitHubLink(ctx context.Context, projectID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM github_links WHERE project_id = $1`, projectID)
	if err != nil {
		return fmt.Errorf("store: delete github link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: github link %s: %w", projectID, ErrNotFound)
	}
	return nil
}

func (s *PostgresStore) TouchGitHubImport(ctx context.Context, projectID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE github_links SET last_import_at = now() WHERE project_id = $1`, projectID)
	if err != nil {
		return fmt.Errorf("store: touch github import: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: github link %s: %w", projectID, ErrNotFound)
	}
	return nil
}

func (s *PostgresStore) UpsertGitHubUserMap(ctx context.Context, m *GitHubUserMap) error {
	if m == nil || strings.TrimSpace(m.GitHubLogin) == "" {
		return errors.New("store: github_login is required")
	}
	login := strings.ToLower(strings.TrimSpace(m.GitHubLogin))
	_, err := s.pool.Exec(ctx, `
		INSERT INTO github_user_map (github_login, github_id, user_id, email, updated_at)
		VALUES ($1, NULLIF($2, 0), NULLIF($3, '')::UUID, NULLIF($4, ''), now())
		ON CONFLICT (github_login) DO UPDATE SET
		  github_id = COALESCE(EXCLUDED.github_id, github_user_map.github_id),
		  user_id = COALESCE(EXCLUDED.user_id, github_user_map.user_id),
		  email = COALESCE(EXCLUDED.email, github_user_map.email),
		  updated_at = now()`,
		login, m.GitHubID, strings.TrimSpace(m.UserID), strings.TrimSpace(m.Email))
	if err != nil {
		return fmt.Errorf("store: upsert github user map: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetGitHubUserMap(ctx context.Context, login string) (*GitHubUserMap, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	var m GitHubUserMap
	var ghID *int64
	var userID, email *string
	err := s.pool.QueryRow(ctx, `
		SELECT github_login, github_id, user_id::TEXT, email, updated_at
		FROM github_user_map WHERE github_login = $1`, login).Scan(
		&m.GitHubLogin, &ghID, &userID, &email, &m.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: github user %q: %w", login, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get github user map: %w", err)
	}
	if ghID != nil {
		m.GitHubID = *ghID
	}
	if userID != nil {
		m.UserID = *userID
	}
	if email != nil {
		m.Email = *email
	}
	return &m, nil
}
