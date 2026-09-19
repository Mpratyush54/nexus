// API token persistence (issue #161 / migration 018 + 024 agent binding).
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// APIToken is a named, revocable credential owned by a user.
// AgentID when set binds the token to an MCP agent identity (cursor, opencode, …).
type APIToken struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"token_prefix"`
	Scopes     []string   `json:"scopes"`
	AgentID    string     `json:"agent_id,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// APITokenStore manages hashed API tokens over any DBTX.
type APITokenStore struct {
	db DBTX
}

// NewAPITokenStore wires an APITokenStore to any DBTX.
func NewAPITokenStore(db DBTX) *APITokenStore {
	return &APITokenStore{db: db}
}

const apiTokenColumns = `id::TEXT AS id, user_id::TEXT AS user_id, name, token_prefix, ` +
	`COALESCE(scopes, '{}') AS scopes, COALESCE(agent_id, '') AS agent_id, ` +
	`created_at, last_used_at, revoked_at`

func scanAPIToken(row pgx.Row) (*APIToken, error) {
	var t APIToken
	if err := row.Scan(
		&t.ID, &t.UserID, &t.Name, &t.Prefix, &t.Scopes, &t.AgentID,
		&t.CreatedAt, &t.LastUsedAt, &t.RevokedAt,
	); err != nil {
		return nil, err
	}
	if t.Scopes == nil {
		t.Scopes = []string{}
	}
	return &t, nil
}

// Create inserts a hashed token row. tokenHash must already be hashed.
// agentID may be empty for generic user tokens; set it for MCP-bound tokens.
func (s *APITokenStore) Create(ctx context.Context, userID, name, prefix, tokenHash string, scopes []string, agentID string) (*APIToken, error) {
	userID = strings.TrimSpace(userID)
	name = strings.TrimSpace(name)
	prefix = strings.TrimSpace(prefix)
	tokenHash = strings.TrimSpace(tokenHash)
	agentID = strings.TrimSpace(agentID)
	if userID == "" || name == "" || prefix == "" || tokenHash == "" {
		return nil, errors.New("store: user_id, name, prefix, and token_hash are required")
	}
	if scopes == nil {
		scopes = []string{}
	}
	t, err := scanAPIToken(s.db.QueryRow(ctx,
		`INSERT INTO api_tokens (user_id, name, token_prefix, token_hash, scopes, agent_id)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+apiTokenColumns,
		userID, name, prefix, tokenHash, scopes, agentID))
	if err != nil {
		return nil, fmt.Errorf("store: create api token: %w", err)
	}
	return t, nil
}

// ListByUser returns non-revoked tokens for a user (newest first).
func (s *APITokenStore) ListByUser(ctx context.Context, userID string) ([]APIToken, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("store: user_id is required")
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+apiTokenColumns+`
		 FROM api_tokens
		 WHERE user_id = $1 AND revoked_at IS NULL
		 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list api tokens: %w", err)
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list api tokens scan: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list api tokens rows: %w", err)
	}
	if out == nil {
		out = []APIToken{}
	}
	return out, nil
}

// LookupActiveByHash returns an active (non-revoked) token by its hash.
func (s *APITokenStore) LookupActiveByHash(ctx context.Context, tokenHash string) (*APIToken, error) {
	tokenHash = strings.TrimSpace(tokenHash)
	if tokenHash == "" {
		return nil, errors.New("store: token_hash is required")
	}
	t, err := scanAPIToken(s.db.QueryRow(ctx,
		`SELECT `+apiTokenColumns+`
		 FROM api_tokens
		 WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: api token: %w", ErrNotFound)
		}
		return nil, fmt.Errorf("store: lookup api token: %w", err)
	}
	return t, nil
}

// TouchLastUsed updates last_used_at for an active token.
func (s *APITokenStore) TouchLastUsed(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE api_tokens SET last_used_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("store: touch api token: %w", err)
	}
	return nil
}

// Revoke soft-deletes a token owned by userID. Returns ErrNotFound if missing
// or already revoked / wrong owner.
func (s *APITokenStore) Revoke(ctx context.Context, userID, tokenID string) error {
	userID = strings.TrimSpace(userID)
	tokenID = strings.TrimSpace(tokenID)
	if userID == "" || tokenID == "" {
		return errors.New("store: user_id and token id are required")
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE api_tokens SET revoked_at = now()
		 WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, tokenID, userID)
	if err != nil {
		return fmt.Errorf("store: revoke api token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: api token %s: %w", tokenID, ErrNotFound)
	}
	return nil
}
