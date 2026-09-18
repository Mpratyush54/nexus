// User identity and preferences (issue #34, plan §1.1).
//
// The users table shipped in migration 001 but had zero store coverage:
// every other store (projects, workspaces, sessions) references user IDs,
// yet no code path could create or read a user. This file closes that gap
// with a thin CRUD store over the DBTX seam (db.go), following the
// projects.go/workspaces.go conventions: NULL-coalescing column lists,
// pgx.ErrNoRows mapped to wrapped ErrNotFound, and validation before any
// statement so invalid input issues no queries.
//
// Testability: UserStore depends only on DBTX, so unit tests run against a
// scripted fake (users_test.go). Settings is selected as settings::TEXT so
// the JSONB column scans into a plain string, mirroring the events.go
// payload::TEXT convention.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// User rows use the shared User model (models.go); Settings arrives as
// settings::TEXT and is decoded into the Settings map (never nil).
// UserParams carries user identity for Create.
type UserParams struct {
	Username string
	Email    string // optional; "" = NULL
	Settings string // optional raw JSON; "" = '{}'
}

// userColumns selects users with NULLs coalesced (settings falls back to
// '{}' so scans never see NULL) and the UUID formatted as text.
const userColumns = `id::TEXT AS id, ` +
	`username, ` +
	`COALESCE(email, '') AS email, ` +
	`COALESCE(settings::TEXT, '{}') AS settings, ` +
	`created_at`

// scanUser scans a full userColumns row.
func scanUser(row pgx.Row) (*User, error) {
	var u User
	var settings string
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &settings, &u.CreatedAt); err != nil {
		return nil, err
	}
	u.Settings = unmarshalPayload([]byte(settings))
	return &u, nil
}

// normalizeSettings trims the settings document, defaulting "" to '{}'.
// Validity beyond "non-empty" is the database's job (JSONB cast rejects
// malformed documents at insert/update time).
func normalizeSettings(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return strings.TrimSpace(s)
}

// UserStore is user CRUD plus settings management over any DBTX.
type UserStore struct {
	db DBTX
}

// NewUserStore wires a UserStore to any DBTX (pool, transaction, fake).
func NewUserStore(db DBTX) *UserStore {
	return &UserStore{db: db}
}

// Create inserts a user. Username is required (the table enforces UNIQUE);
// email and settings are optional.
func (s *UserStore) Create(ctx context.Context, params UserParams) (*User, error) {
	username := strings.TrimSpace(params.Username)
	if username == "" {
		return nil, errors.New("store: username is required")
	}
	u, err := scanUser(s.db.QueryRow(ctx,
		`INSERT INTO users (username, email, settings)
		 VALUES ($1, $2, $3::JSONB)
		 RETURNING `+userColumns,
		username,
		nullText(strings.TrimSpace(params.Email)),
		normalizeSettings(params.Settings)))
	if err != nil {
		return nil, fmt.Errorf("store: create user: %w", err)
	}
	return u, nil
}

// GetByID fetches one user or a wrapped ErrNotFound.
func (s *UserStore) GetByID(ctx context.Context, id string) (*User, error) {
	u, err := scanUser(s.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: user %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get user: %w", err)
	}
	return u, nil
}

// GetByUsername fetches one user by its unique username or a wrapped
// ErrNotFound. This is the daemon/server login path: workspace registration
// carries a username, not a UUID.
func (s *UserStore) GetByUsername(ctx context.Context, username string) (*User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("store: username is required")
	}
	u, err := scanUser(s.db.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = $1`, username))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: user %q: %w", username, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get user by username: %w", err)
	}
	return u, nil
}

// UpdateSettings replaces the whole settings document (LLM provider, API
// key ref, preferences) and returns the updated row.
func (s *UserStore) UpdateSettings(ctx context.Context, id, settings string) (*User, error) {
	u, err := scanUser(s.db.QueryRow(ctx,
		`UPDATE users SET settings = $2::JSONB WHERE id = $1 RETURNING `+userColumns,
		id, normalizeSettings(settings)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: user %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: update user settings: %w", err)
	}
	return u, nil
}

// SetPasswordHash stores a pre-hashed password for login verification
// (issue #133; migration 011). The hash itself is produced by the server
// (PBKDF2-SHA256, see internal/server/auth.go) or the nexus CLI —
// plaintext passwords never reach the store layer.
func (s *UserStore) SetPasswordHash(ctx context.Context, id, hash string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("store: user id is required")
	}
	if strings.TrimSpace(hash) == "" {
		return errors.New("store: password hash is required")
	}
	tag, err := s.db.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, id, strings.TrimSpace(hash))
	if err != nil {
		return fmt.Errorf("store: set password hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: user %s: %w", id, ErrNotFound)
	}
	return nil
}

// GetPasswordHashByID returns the stored password hash for a user id.
func (s *UserStore) GetPasswordHashByID(ctx context.Context, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("store: user id is required")
	}
	var hash *string
	if err := s.db.QueryRow(ctx,
		`SELECT password_hash FROM users WHERE id = $1`, id).Scan(&hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("store: user %s: %w", id, ErrNotFound)
		}
		return "", fmt.Errorf("store: get password hash by id: %w", err)
	}
	if hash == nil {
		return "", nil
	}
	return *hash, nil
}

// UpdateProfile updates optional email and/or settings for a user.
// Empty email clears the column (NULL). Pass settings="" to leave settings unchanged.
func (s *UserStore) UpdateProfile(ctx context.Context, id string, email *string, settings *string) (*User, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("store: user id is required")
	}
	if email == nil && settings == nil {
		return s.GetByID(ctx, id)
	}

	setParts := make([]string, 0, 2)
	args := []any{id}
	argN := 2
	if email != nil {
		setParts = append(setParts, fmt.Sprintf("email = $%d", argN))
		args = append(args, nullText(strings.TrimSpace(*email)))
		argN++
	}
	if settings != nil {
		setParts = append(setParts, fmt.Sprintf("settings = $%d::JSONB", argN))
		args = append(args, normalizeSettings(*settings))
		argN++
	}

	q := `UPDATE users SET ` + strings.Join(setParts, ", ") + ` WHERE id = $1 RETURNING ` + userColumns
	u, err := scanUser(s.db.QueryRow(ctx, q, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: user %s: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: update user profile: %w", err)
	}
	return u, nil
}

// UsageStats aggregates per-user activity counters for GET /users/me/usage.
type UsageStats struct {
	MemoriesCreated int64 `json:"memories_created"`
	EpisodesCreated int64 `json:"episodes_created"`
	RequestsApprox  int64 `json:"requests_approx"`
}

// GetUsage returns simple aggregate counts for a user.
func (s *UserStore) GetUsage(ctx context.Context, userID string) (*UsageStats, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("store: user id is required")
	}
	var stats UsageStats
	err := s.db.QueryRow(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM memory_items WHERE proposed_by::TEXT = $1 OR user_id::TEXT = $1),
		  (SELECT COUNT(*) FROM episodes WHERE created_by::TEXT = $1)
	`, userID).Scan(&stats.MemoriesCreated, &stats.EpisodesCreated)
	if err != nil {
		return nil, fmt.Errorf("store: user usage: %w", err)
	}
	// No request counter table yet — approximate as memory+episode writes.
	stats.RequestsApprox = stats.MemoriesCreated + stats.EpisodesCreated
	return &stats, nil
}

// GetPasswordHash returns the user id + stored hash for login verification.
// Empty hash means no password set (login must 401, never accept).
func (s *UserStore) GetPasswordHash(ctx context.Context, username string) (string, string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", "", errors.New("store: username is required")
	}
	var id string
	var hash *string
	if err := s.db.QueryRow(ctx,
		`SELECT id::TEXT AS id, password_hash FROM users WHERE username = $1`, username).Scan(&id, &hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("store: user %q: %w", username, ErrNotFound)
		}
		return "", "", fmt.Errorf("store: get password hash: %w", err)
	}
	if hash == nil {
		return id, "", nil
	}
	return id, *hash, nil
}

// Delete removes a user. Rows referencing the user (projects.created_by,
// workspaces.user_id) block deletion with a foreign-key error —
// reassignment is a follow-up.
func (s *UserStore) Delete(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("store: delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: user %s: %w", id, ErrNotFound)
	}
	return nil
}

// List returns users newest-first with a clamped limit.
func (s *UserStore) List(ctx context.Context, limit, offset int) ([]User, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, fmt.Errorf("store: list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list users scan: %w", err)
		}
		out = append(out, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list users rows: %w", err)
	}
	return out, nil
}
