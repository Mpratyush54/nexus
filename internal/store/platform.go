package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"central-memory/internal/buildinfo"
)

var (
	_ PlatformStore = (*MemStore)(nil)
	_ PlatformStore = (*PostgresStore)(nil)
)

// ReleaseArtifact is one downloadable binary (or PWA tarball) for an app version.
type ReleaseArtifact struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	URL      string `json:"url"`
	Filename string `json:"filename,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// AppRelease is one published version of api, pwa, cli, or daemon.
type AppRelease struct {
	ID          string            `json:"id"`
	App         string            `json:"app"`
	Version     string            `json:"version"`
	Channel     string            `json:"channel"`
	Notes       string            `json:"notes,omitempty"`
	GitSHA      string            `json:"git_sha,omitempty"`
	Artifacts   []ReleaseArtifact `json:"artifacts,omitempty"`
	PublishedBy string            `json:"published_by,omitempty"`
	PublishedAt time.Time         `json:"published_at"`
	Yanked      bool              `json:"yanked,omitempty"`
}

// PlatformStats is a Super Admin overview snapshot.
type PlatformStats struct {
	Users    int `json:"users"`
	Orgs     int `json:"orgs"`
	Projects int `json:"projects"`
}

// PlatformStore is the Super Admin + release registry surface. Both
// MemStore and PostgresStore implement it; the core Store interface stays
// unchanged (same seam as RoleStore / orgStore).
type PlatformStore interface {
	IsPlatformAdmin(ctx context.Context, userID string) (bool, error)
	SetPlatformAdmin(ctx context.Context, userID, grantedBy string, admin bool) error
	ListPlatformAdmins(ctx context.Context) ([]string, error)
	PlatformStats(ctx context.Context) (*PlatformStats, error)
	UpsertRelease(ctx context.Context, rel *AppRelease) error
	ListReleases(ctx context.Context, app, channel string, includeYanked bool) ([]*AppRelease, error)
	LatestRelease(ctx context.Context, app, channel string) (*AppRelease, error)
	YankRelease(ctx context.Context, app, version string) error
}

func NormalizeReleaseApp(app string) string {
	switch strings.ToLower(strings.TrimSpace(app)) {
	case buildinfo.AppAPI, buildinfo.AppPWA, buildinfo.AppCLI, buildinfo.AppDaemon, buildinfo.AppDesktop:
		return strings.ToLower(strings.TrimSpace(app))
	default:
		return ""
	}
}

func NormalizeReleaseChannel(ch string) string {
	ch = strings.ToLower(strings.TrimSpace(ch))
	if ch == "" {
		return "stable"
	}
	return ch
}

func cloneRelease(r *AppRelease) *AppRelease {
	if r == nil {
		return nil
	}
	out := *r
	if r.Artifacts != nil {
		out.Artifacts = append([]ReleaseArtifact(nil), r.Artifacts...)
	}
	return &out
}

func (s *MemStore) IsPlatformAdmin(_ context.Context, userID string) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.platformAdmins[userID], nil
}

func (s *MemStore) SetPlatformAdmin(_ context.Context, userID, grantedBy string, admin bool) error {
	_ = grantedBy
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("store: user id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.platformAdmins == nil {
		s.platformAdmins = map[string]bool{}
	}
	if admin {
		s.platformAdmins[userID] = true
	} else {
		delete(s.platformAdmins, userID)
	}
	return nil
}

func (s *MemStore) ListPlatformAdmins(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.platformAdmins))
	for id, ok := range s.platformAdmins {
		if ok {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *MemStore) PlatformStats(_ context.Context) (*PlatformStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &PlatformStats{
		Users:    len(s.platformAdmins),
		Orgs:     len(s.orgs),
		Projects: len(s.projects),
	}, nil
}

func (s *MemStore) UpsertRelease(_ context.Context, rel *AppRelease) error {
	if rel == nil {
		return fmt.Errorf("store: release is required")
	}
	app := NormalizeReleaseApp(rel.App)
	ver := strings.TrimSpace(rel.Version)
	if app == "" || ver == "" {
		return fmt.Errorf("store: app and version are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := cloneRelease(rel)
	stored.App = app
	stored.Version = ver
	stored.Channel = NormalizeReleaseChannel(rel.Channel)
	if stored.PublishedAt.IsZero() {
		stored.PublishedAt = time.Now().UTC()
	}
	if stored.ID == "" {
		stored.ID = newID("rel")
	}
	if s.releases == nil {
		s.releases = map[string]*AppRelease{}
	}
	key := app + "@" + ver
	s.releases[key] = stored
	*rel = *cloneRelease(stored)
	return nil
}

func (s *MemStore) ListReleases(_ context.Context, app, channel string, includeYanked bool) ([]*AppRelease, error) {
	app = NormalizeReleaseApp(app)
	channel = strings.TrimSpace(channel)
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*AppRelease, 0, len(s.releases))
	for _, rel := range s.releases {
		if app != "" && rel.App != app {
			continue
		}
		if channel != "" && rel.Channel != channel {
			continue
		}
		if rel.Yanked && !includeYanked {
			continue
		}
		out = append(out, cloneRelease(rel))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].PublishedAt.After(out[j].PublishedAt)
	})
	return out, nil
}

func (s *MemStore) LatestRelease(ctx context.Context, app, channel string) (*AppRelease, error) {
	list, err := s.ListReleases(ctx, app, NormalizeReleaseChannel(channel), false)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

func (s *MemStore) YankRelease(_ context.Context, app, version string) error {
	app = NormalizeReleaseApp(app)
	version = strings.TrimSpace(version)
	s.mu.Lock()
	defer s.mu.Unlock()
	rel, ok := s.releases[app+"@"+version]
	if !ok {
		return ErrNotFound
	}
	rel.Yanked = true
	return nil
}

func (s *PostgresStore) IsPlatformAdmin(ctx context.Context, userID string) (bool, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM platform_admins WHERE user_id = $1::uuid)`, userID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("store: is platform admin: %w", err)
	}
	return ok, nil
}

func (s *PostgresStore) SetPlatformAdmin(ctx context.Context, userID, grantedBy string, admin bool) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("store: user id is required")
	}
	if !admin {
		_, err := s.pool.Exec(ctx, `DELETE FROM platform_admins WHERE user_id = $1::uuid`, userID)
		if err != nil {
			return fmt.Errorf("store: revoke platform admin: %w", err)
		}
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO platform_admins (user_id, granted_by)
		 VALUES ($1::uuid, $2)
		 ON CONFLICT (user_id) DO NOTHING`,
		userID, nullUUID(strings.TrimSpace(grantedBy)))
	if err != nil {
		return fmt.Errorf("store: grant platform admin: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListPlatformAdmins(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT user_id::TEXT FROM platform_admins ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: list platform admins: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *PostgresStore) PlatformStats(ctx context.Context) (*PlatformStats, error) {
	var st PlatformStats
	err := s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM users),
		  (SELECT COUNT(*) FROM organizations),
		  (SELECT COUNT(*) FROM projects)`).Scan(&st.Users, &st.Orgs, &st.Projects)
	if err != nil {
		return nil, fmt.Errorf("store: platform stats: %w", err)
	}
	return &st, nil
}

func (s *PostgresStore) UpsertRelease(ctx context.Context, rel *AppRelease) error {
	if rel == nil {
		return fmt.Errorf("store: release is required")
	}
	app := NormalizeReleaseApp(rel.App)
	ver := strings.TrimSpace(rel.Version)
	if app == "" || ver == "" {
		return fmt.Errorf("store: app and version are required")
	}
	channel := NormalizeReleaseChannel(rel.Channel)
	art, err := json.Marshal(rel.Artifacts)
	if err != nil {
		return fmt.Errorf("store: marshal artifacts: %w", err)
	}
	if len(rel.Artifacts) == 0 {
		art = []byte("[]")
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO app_releases (app, version, channel, notes, git_sha, artifacts, published_by)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)
		ON CONFLICT (app, version) DO UPDATE SET
		  channel = EXCLUDED.channel,
		  notes = EXCLUDED.notes,
		  git_sha = EXCLUDED.git_sha,
		  artifacts = EXCLUDED.artifacts,
		  yanked = false,
		  published_at = now()
		RETURNING id::TEXT, published_at`,
		app, ver, channel,
		nullText(strings.TrimSpace(rel.Notes)),
		nullText(strings.TrimSpace(rel.GitSHA)),
		string(art),
		nullText(strings.TrimSpace(rel.PublishedBy)))
	if err := row.Scan(&rel.ID, &rel.PublishedAt); err != nil {
		return fmt.Errorf("store: upsert release: %w", err)
	}
	rel.App = app
	rel.Version = ver
	rel.Channel = channel
	rel.Yanked = false
	return nil
}

func scanRelease(id, app, version, channel, notes, gitSHA, artifactsJSON, publishedBy string, publishedAt time.Time, yanked bool) (*AppRelease, error) {
	rel := &AppRelease{
		ID:          id,
		App:         app,
		Version:     version,
		Channel:     channel,
		Notes:       notes,
		GitSHA:      gitSHA,
		PublishedBy: publishedBy,
		PublishedAt: publishedAt,
		Yanked:      yanked,
	}
	if strings.TrimSpace(artifactsJSON) != "" && artifactsJSON != "null" {
		if err := json.Unmarshal([]byte(artifactsJSON), &rel.Artifacts); err != nil {
			return nil, err
		}
	}
	return rel, nil
}

func (s *PostgresStore) ListReleases(ctx context.Context, app, channel string, includeYanked bool) ([]*AppRelease, error) {
	app = NormalizeReleaseApp(app)
	channel = strings.TrimSpace(channel)
	q := `SELECT id::TEXT, app, version, channel,
	             COALESCE(notes, ''), COALESCE(git_sha, ''),
	             COALESCE(artifacts::TEXT, '[]'),
	             COALESCE(published_by::TEXT, ''), published_at, yanked
	        FROM app_releases WHERE 1=1`
	args := []any{}
	n := 1
	if app != "" {
		q += fmt.Sprintf(" AND app = $%d", n)
		args = append(args, app)
		n++
	}
	if channel != "" {
		q += fmt.Sprintf(" AND channel = $%d", n)
		args = append(args, channel)
		n++
	}
	if !includeYanked {
		q += " AND yanked = false"
	}
	q += " ORDER BY published_at DESC"
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list releases: %w", err)
	}
	defer rows.Close()
	var out []*AppRelease
	for rows.Next() {
		var id, a, ver, ch, notes, sha, art, by string
		var at time.Time
		var yanked bool
		if err := rows.Scan(&id, &a, &ver, &ch, &notes, &sha, &art, &by, &at, &yanked); err != nil {
			return nil, err
		}
		rel, err := scanRelease(id, a, ver, ch, notes, sha, art, by, at, yanked)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	return out, rows.Err()
}

func (s *PostgresStore) LatestRelease(ctx context.Context, app, channel string) (*AppRelease, error) {
	list, err := s.ListReleases(ctx, app, NormalizeReleaseChannel(channel), false)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

func (s *PostgresStore) YankRelease(ctx context.Context, app, version string) error {
	app = NormalizeReleaseApp(app)
	version = strings.TrimSpace(version)
	tag, err := s.pool.Exec(ctx,
		`UPDATE app_releases SET yanked = true WHERE app = $1 AND version = $2`,
		app, version)
	if err != nil {
		return fmt.Errorf("store: yank release: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
