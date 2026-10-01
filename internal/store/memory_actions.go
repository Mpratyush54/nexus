package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"central-memory/internal/memoryact"
)

// Memory action errors. Handlers map ErrMemoryActionForbidden to 403 and
// ErrInvalidMemoryAction to 400.
var (
	ErrMemoryActionForbidden = errors.New("memory action forbidden")
	ErrInvalidMemoryAction   = errors.New("invalid memory action")
)

// statusForgotten is the MemStore literal. Postgres CHECK
// (migrations/001_initial.up.sql) allows only PROPOSED, CONFIRMED, REJECTED,
// and SUPERSEDED, so PostgresStore writes REJECTED. memoryact.PublicStatus
// maps that REJECTED row to forgotten.
const statusForgotten = "forgotten"

// pinnedTag is stored on memory_items.tags. There is no pinned column.
const pinnedTag = "pinned"

// Promotion is the provenance recorded for an auto-promoted fact.
type Promotion = memoryact.Provenance

// memoryPins and memoryPromotions overlay MemStore without adding fields to
// the struct (store.go stays unchanged). Postgres keeps pin on tags and
// provenance in context_snippet.
var (
	memoryActionMu   sync.Mutex
	memoryPins       = map[string]bool{}
	memoryPromotions = map[string]Promotion{}
)

// MemoryActionStore is pin, scope, forget, promote, and owner removal.
type MemoryActionStore interface {
	PinMemory(ctx context.Context, id string) (*MemoryItem, error)
	SetMemoryScope(ctx context.Context, id, level string) (*MemoryItem, error)
	ForgetMemory(ctx context.Context, id string) (*MemoryItem, error)
	PromoteMemory(ctx context.Context, id string) (*PromoteOutcome, error)
	RemovePromotedMemory(ctx context.Context, id, actorID string) (*MemoryItem, error)
}

// PromoteOutcome is a promotion attempt. Held means the fact stayed at
// session level because redaction found an unstrippable secret.
type PromoteOutcome struct {
	Item      *MemoryItem
	Held      bool
	Promotion Promotion
}

// MemoryPinned reports the MemStore pin flag. Postgres pins live on tags.
func MemoryPinned(id string) bool {
	memoryActionMu.Lock()
	defer memoryActionMu.Unlock()
	return memoryPins[id]
}

func withTag(tags []string, tag string) []string {
	for _, t := range tags {
		if t == tag {
			if tags == nil {
				return []string{}
			}
			return tags
		}
	}
	out := make([]string, len(tags)+1)
	copy(out, tags)
	out[len(tags)] = tag
	return out
}

func assignMemoryLevel(item *MemoryItem, level string) error {
	canon, ok := memoryact.CanonicalLevel(level)
	if !ok {
		return fmt.Errorf("%w: unknown level %q", ErrInvalidMemoryAction, strings.TrimSpace(level))
	}
	switch canon {
	case LevelPersonal:
		if strings.TrimSpace(item.UserID) == "" {
			return fmt.Errorf("%w: personal scope requires user_id", ErrInvalidMemoryAction)
		}
	case LevelSession:
		if strings.TrimSpace(item.SessionID) == "" {
			return fmt.Errorf("%w: session scope requires session_id", ErrInvalidMemoryAction)
		}
	}
	item.Level = canon
	// A non-session row must not keep session_id (ValidateMemoryScopeRules).
	// Promotion provenance keeps the id separately.
	if canon != LevelSession {
		item.SessionID = ""
	}
	item.UpdatedAt = time.Now().UTC()
	return nil
}

func promotable(status string) error {
	switch memoryact.PublicStatus(status) {
	case memoryact.PublicForgotten, memoryact.PublicSuperseded:
		return fmt.Errorf("%w: memory cannot be promoted from status %s", ErrInvalidMemoryAction, memoryact.PublicStatus(status))
	default:
		return nil
	}
}

// ---- MemStore ----

func (s *MemStore) PinMemory(ctx context.Context, id string) (*MemoryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.memoryByID(id)
	if err != nil {
		return nil, err
	}
	memoryActionMu.Lock()
	memoryPins[id] = true
	memoryActionMu.Unlock()
	item.Tags = withTag(item.Tags, pinnedTag)
	item.UpdatedAt = time.Now().UTC()
	return cloneMemoryItem(item), nil
}

func (s *MemStore) SetMemoryScope(ctx context.Context, id, level string) (*MemoryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.memoryByID(id)
	if err != nil {
		return nil, err
	}
	if err := assignMemoryLevel(item, level); err != nil {
		return nil, err
	}
	return cloneMemoryItem(item), nil
}

func (s *MemStore) ForgetMemory(ctx context.Context, id string) (*MemoryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.memoryByID(id)
	if err != nil {
		return nil, err
	}
	item.Status = statusForgotten
	item.UpdatedAt = time.Now().UTC()
	return cloneMemoryItem(item), nil
}

func (s *MemStore) PromoteMemory(ctx context.Context, id string) (*PromoteOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.memoryByID(id)
	if err != nil {
		return nil, err
	}
	if err := promotable(item.Status); err != nil {
		return nil, err
	}
	if item.Level != LevelSession {
		memoryActionMu.Lock()
		promo := memoryPromotions[id]
		memoryActionMu.Unlock()
		return &PromoteOutcome{Item: cloneMemoryItem(item), Promotion: promo}, nil
	}
	red := memoryact.RedactForPromotion(item.Content)
	if red.Held {
		return &PromoteOutcome{Item: cloneMemoryItem(item), Held: true}, nil
	}
	if err := ValidateMemoryContent(red.Text); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidMemoryAction, err.Error())
	}
	promo := Promotion{
		SourceSessionID:     item.SessionID,
		SourceOwnerID:       item.UserID,
		PromotedFromPrivate: true,
	}
	item.Content = red.Text
	item.Level = LevelProject
	item.SessionID = ""
	item.ContextSnippet = memoryact.FormatProvenancePrefix(promo, item.ContextSnippet)
	item.UpdatedAt = time.Now().UTC()
	memoryActionMu.Lock()
	memoryPromotions[id] = promo
	memoryActionMu.Unlock()
	return &PromoteOutcome{Item: cloneMemoryItem(item), Promotion: promo}, nil
}

func (s *MemStore) RemovePromotedMemory(ctx context.Context, id, actorID string) (*MemoryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	actorID = strings.TrimSpace(actorID)
	s.mu.Lock()
	defer s.mu.Unlock()
	item, err := s.memoryByID(id)
	if err != nil {
		return nil, err
	}
	if actorID == "" || item.UserID == "" || item.UserID != actorID {
		return nil, ErrMemoryActionForbidden
	}
	memoryActionMu.Lock()
	promo := memoryPromotions[id]
	if promo.SourceOwnerID == "" {
		promo.SourceOwnerID = item.UserID
	}
	if promo.SourceSessionID == "" {
		promo.SourceSessionID = item.SessionID
	}
	promo.RemovedByOwner = true
	memoryPromotions[id] = promo
	memoryActionMu.Unlock()
	item.Status = statusForgotten
	item.ContextSnippet = memoryact.FormatProvenancePrefix(promo, item.ContextSnippet)
	item.SessionID = ""
	item.UpdatedAt = time.Now().UTC()
	return cloneMemoryItem(item), nil
}

func (s *MemStore) memoryByID(id string) (*MemoryItem, error) {
	item, ok := s.memories[id]
	if !ok || item == nil {
		return nil, ErrNotFound
	}
	return item, nil
}

// ---- PostgresStore ----
//
// Columns written here already exist on memory_items: level, status, content,
// tags, context_snippet, session_id. Pin has no column, so it is a "pinned"
// tag. Provenance has no columns, so it is a context_snippet prefix.
// status CHECK rejects "forgotten"; forget and owner removal write REJECTED.

func (s *PostgresStore) PinMemory(ctx context.Context, id string) (*MemoryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := s.requireMemoryActionItem(ctx, id); err != nil {
		return nil, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE memory_items
		   SET tags = CASE
		           WHEN COALESCE(tags, '{}'::text[]) @> ARRAY['pinned']::text[] THEN tags
		           ELSE COALESCE(tags, '{}'::text[]) || ARRAY['pinned']::text[]
		       END,
		       updated_at = now()
		 WHERE id = $1::uuid`, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetMemoryItem(ctx, id)
}

func (s *PostgresStore) SetMemoryScope(ctx context.Context, id, level string) (*MemoryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	item, err := s.requireMemoryActionItem(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := assignMemoryLevel(item, level); err != nil {
		return nil, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE memory_items
		   SET level = $2,
		       session_id = CASE WHEN $2 = 'session' THEN session_id ELSE NULL END,
		       updated_at = now()
		 WHERE id = $1::uuid`, strings.TrimSpace(id), item.Level)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetMemoryItem(ctx, id)
}

func (s *PostgresStore) ForgetMemory(ctx context.Context, id string) (*MemoryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := s.requireMemoryActionItem(ctx, id); err != nil {
		return nil, err
	}
	// CHECK (status IN ('PROPOSED','CONFIRMED','REJECTED','SUPERSEDED')).
	tag, err := s.pool.Exec(ctx, `
		UPDATE memory_items
		   SET status = 'REJECTED', updated_at = now()
		 WHERE id = $1::uuid`, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetMemoryItem(ctx, id)
}

func (s *PostgresStore) PromoteMemory(ctx context.Context, id string) (*PromoteOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	item, err := s.requireMemoryActionItem(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := promotable(item.Status); err != nil {
		return nil, err
	}
	if item.Level != LevelSession {
		parsed, _ := memoryact.ParseProvenancePrefix(item.ContextSnippet)
		return &PromoteOutcome{Item: item, Promotion: parsed}, nil
	}
	red := memoryact.RedactForPromotion(item.Content)
	if red.Held {
		return &PromoteOutcome{Item: item, Held: true}, nil
	}
	if err := ValidateMemoryContent(red.Text); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidMemoryAction, err.Error())
	}
	promo := Promotion{
		SourceSessionID:     item.SessionID,
		SourceOwnerID:       item.UserID,
		PromotedFromPrivate: true,
	}
	snippet := memoryact.FormatProvenancePrefix(promo, item.ContextSnippet)
	tag, err := s.pool.Exec(ctx, `
		UPDATE memory_items
		   SET level = 'project',
		       content = $2,
		       session_id = NULL,
		       context_snippet = $3,
		       updated_at = now()
		 WHERE id = $1::uuid`, strings.TrimSpace(id), red.Text, snippet)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	updated, err := s.GetMemoryItem(ctx, id)
	if err != nil {
		return nil, err
	}
	return &PromoteOutcome{Item: updated, Promotion: promo}, nil
}

func (s *PostgresStore) RemovePromotedMemory(ctx context.Context, id, actorID string) (*MemoryItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	item, err := s.requireMemoryActionItem(ctx, id)
	if err != nil {
		return nil, err
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" || item.UserID == "" || item.UserID != actorID {
		return nil, ErrMemoryActionForbidden
	}
	promo, _ := memoryact.ParseProvenancePrefix(item.ContextSnippet)
	if promo.SourceOwnerID == "" {
		promo.SourceOwnerID = item.UserID
	}
	if promo.SourceSessionID == "" {
		promo.SourceSessionID = item.SessionID
	}
	promo.RemovedByOwner = true
	snippet := memoryact.FormatProvenancePrefix(promo, item.ContextSnippet)
	// CHECK rejects the literal forgotten; PublicStatus maps REJECTED to forgotten.
	tag, err := s.pool.Exec(ctx, `
		UPDATE memory_items
		   SET status = 'REJECTED',
		       context_snippet = $2,
		       session_id = NULL,
		       updated_at = now()
		 WHERE id = $1::uuid`, strings.TrimSpace(id), snippet)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetMemoryItem(ctx, id)
}

func (s *PostgresStore) requireMemoryActionItem(ctx context.Context, id string) (*MemoryItem, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: memory id is required", ErrInvalidMemoryAction)
	}
	return s.GetMemoryItem(ctx, id)
}

var (
	_ MemoryActionStore = (*MemStore)(nil)
	_ MemoryActionStore = (*PostgresStore)(nil)
)
