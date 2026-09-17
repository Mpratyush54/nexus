// Tests for issue #38: memory lifecycle transitions
// (POST /memory/{id}/confirm|reject|promote).
//
// The frozen Store seam (server.go) has no Get/Update methods, so the test
// fake embeds fakeStore (server_test.go) and adds the lifecycleStore pair.
// Handlers type-assert to lifecycleStore; this proves the extension pattern
// works without widening Store.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

// lifecycleFake is a fakeStore plus the issue #38 lifecycleStore pair.
// Memories are value rows keyed by ID; Get/Update are mutex-guarded.
type lifecycleFake struct {
	*fakeStore
}

func (f *lifecycleFake) GetMemory(_ context.Context, id string) (*Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.memories {
		if m.ID == id {
			cp := m
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("server: memory %s: %w", id, store.ErrNotFound)
}

func (f *lifecycleFake) UpdateMemory(_ context.Context, m Memory) (*Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, cur := range f.memories {
		if cur.ID == m.ID {
			f.memories[i] = m
			cp := m
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("server: memory %s: %w", m.ID, store.ErrNotFound)
}

// lifecycleHarness mirrors newHarness but wires the lifecycleFake so the
// three transition handlers resolve their optional backend.
func lifecycleHarness(t *testing.T) (*harness, *lifecycleFake) {
	t.Helper()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	h := &harness{now: now}
	lf := &lifecycleFake{fakeStore: newFakeStore(func() time.Time { return h.now })}
	h.fake = lf.fakeStore
	h.srv = New(lf, Options{
		JWTSecret: []byte("test-secret-that-is-long-enough-32B!"),
		TokenTTL:  time.Hour,
		Now:       func() time.Time { return h.now },
	})
	h.token = h.login(t, "alice", "s3cret")
	return h, lf
}

// seedMemory inserts a memory row with a fixed ID directly (no HTTP), so
// each test controls status/level exactly.
func seedMemory(h *harness, id, status, level string) {
	h.fake.mu.Lock()
	defer h.fake.mu.Unlock()
	h.fake.memories = append(h.fake.memories, Memory{
		ID:         id,
		ProjectID:  "proj-1",
		Key:        "lifecycle/" + id,
		Content:    "Lifecycle fixture content, long enough to be valid.",
		Level:      level,
		Scope:      "fact",
		Confidence: 1.0,
		Status:     status,
		CreatedAt:  h.now.UTC().Format(time.RFC3339),
	})
}

func decodeMemory(t *testing.T, raw []byte) Memory {
	t.Helper()
	var m Memory
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode memory: %v body=%s", err, raw)
	}
	return m
}

func TestMemoryConfirmLifecycle(t *testing.T) {
	h, _ := lifecycleHarness(t)
	seedMemory(h, "mem-confirm", store.StatusProposed, store.LevelProject)

	status, raw := h.do(t, "POST", "/memory/mem-confirm/confirm", map[string]any{}, true)
	if status != http.StatusOK {
		t.Fatalf("confirm: status=%d body=%s", status, raw)
	}
	if got := decodeMemory(t, raw).Status; got != store.StatusConfirmed {
		t.Fatalf("confirm: status = %q, want CONFIRMED", got)
	}

	// Second confirm is an invalid-state transition → 409.
	status, _ = h.do(t, "POST", "/memory/mem-confirm/confirm", map[string]any{}, true)
	if status != http.StatusConflict {
		t.Fatalf("re-confirm: status = %d, want 409", status)
	}
}

func TestMemoryRejectLifecycle(t *testing.T) {
	h, _ := lifecycleHarness(t)
	seedMemory(h, "mem-reject", store.StatusProposed, store.LevelProject)

	status, raw := h.do(t, "POST", "/memory/mem-reject/reject", map[string]any{}, true)
	if status != http.StatusOK {
		t.Fatalf("reject: status=%d body=%s", status, raw)
	}
	if got := decodeMemory(t, raw).Status; got != store.StatusRejected {
		t.Fatalf("reject: status = %q, want REJECTED", got)
	}

	// Confirm-after-reject and reject-after-reject are both invalid → 409.
	if status, _ := h.do(t, "POST", "/memory/mem-reject/confirm", map[string]any{}, true); status != http.StatusConflict {
		t.Fatalf("confirm-after-reject: status = %d, want 409", status)
	}
	if status, _ := h.do(t, "POST", "/memory/mem-reject/reject", map[string]any{}, true); status != http.StatusConflict {
		t.Fatalf("re-reject: status = %d, want 409", status)
	}
}

func TestMemoryRejectAfterConfirm409(t *testing.T) {
	h, _ := lifecycleHarness(t)
	seedMemory(h, "mem-confirmed", store.StatusConfirmed, store.LevelProject)

	if status, _ := h.do(t, "POST", "/memory/mem-confirmed/reject", map[string]any{}, true); status != http.StatusConflict {
		t.Fatalf("reject-after-confirm: status = %d, want 409", status)
	}
	if status, _ := h.do(t, "POST", "/memory/mem-confirmed/confirm", map[string]any{}, true); status != http.StatusConflict {
		t.Fatalf("re-confirm: status = %d, want 409", status)
	}
}

func TestMemoryPromoteLifecycle(t *testing.T) {
	h, _ := lifecycleHarness(t)
	seedMemory(h, "mem-session", store.StatusConfirmed, store.LevelSession)

	// web/ sends {level: nextLevel(...)}; the handler ignores the body and
	// always promotes session → project, so send the real frontend shape.
	status, raw := h.do(t, "POST", "/memory/mem-session/promote",
		map[string]any{"level": "personal"}, true)
	if status != http.StatusOK {
		t.Fatalf("promote: status=%d body=%s", status, raw)
	}
	if got := decodeMemory(t, raw).Level; got != store.LevelProject {
		t.Fatalf("promote: level = %q, want project", got)
	}

	// Promoting a non-session memory is invalid → 409.
	seedMemory(h, "mem-project", store.StatusConfirmed, store.LevelProject)
	if status, _ := h.do(t, "POST", "/memory/mem-project/promote", map[string]any{}, true); status != http.StatusConflict {
		t.Fatalf("promote project-level: status = %d, want 409", status)
	}
}

func TestMemoryLifecycleUnknownID404(t *testing.T) {
	h, _ := lifecycleHarness(t)
	for _, action := range []string{"confirm", "reject", "promote"} {
		if status, raw := h.do(t, "POST", "/memory/no-such-id/"+action, map[string]any{}, true); status != http.StatusNotFound {
			t.Fatalf("%s unknown id: status = %d, want 404 body=%s", action, status, raw)
		}
	}
}

func TestMemoryLifecycleRequiresAuth(t *testing.T) {
	h, _ := lifecycleHarness(t)
	seedMemory(h, "mem-auth", store.StatusProposed, store.LevelProject)
	if status, _ := h.do(t, "POST", "/memory/mem-auth/confirm", map[string]any{}, false); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated confirm: status = %d, want 401", status)
	}
}

func TestMemoryLifecycleErrorEnvelope(t *testing.T) {
	h, _ := lifecycleHarness(t)
	seedMemory(h, "mem-env", store.StatusConfirmed, store.LevelProject)
	status, raw := h.do(t, "POST", "/memory/mem-env/confirm", map[string]any{}, true)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || strings.TrimSpace(env.Error) == "" {
		t.Fatalf("409 must carry {\"error\": ...}, got %s", raw)
	}
}
