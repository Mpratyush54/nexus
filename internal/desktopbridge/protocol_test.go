package desktopbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClaimOnceCancellationAndIsolation(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	now := time.Now().UTC()
	d := Device{ID: "dev", Owner: "alice", KeyHash: HashKey(strings.Repeat("a", 64)), Projects: []string{"project"}}
	if err := m.Register(ctx, d); err != nil {
		t.Fatal(err)
	}
	if !d.Authenticate(strings.Repeat("a", 64)) || d.Authenticate(strings.Repeat("b", 64)) {
		t.Fatal("device credential check")
	}
	j := Job{Request: Request{ID: "job", DeviceID: "dev", ProjectID: "project", SessionID: "session", Kind: "turn", Agent: "codex", Prompt: "hello"}, Owner: "alice", State: "queued", Revision: 1, Created: now, Expires: now.Add(time.Minute)}
	if _, e := m.Create(ctx, j); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Get(ctx, "bob", j.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("cross-account read", e)
	}
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, e := m.Claim(ctx, "alice", "dev", now)
			if e != nil {
				t.Error(e)
			}
			if claim != nil {
				count.Add(1)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("claims=%d", count.Load())
	}
	got, _ := m.Get(ctx, "alice", "job")
	if _, e := m.Update(ctx, "alice", "other", "job", Update{Revision: got.Revision, State: "running"}, now); !errors.Is(e, ErrNotFound) {
		t.Fatal("wrong device update", e)
	}
	running, e := m.Update(ctx, "alice", "dev", "job", Update{Revision: got.Revision, State: "running"}, now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Update(ctx, "alice", "dev", "job", Update{Revision: got.Revision, State: "completed"}, now); !errors.Is(e, ErrConflict) {
		t.Fatal("stale progress accepted")
	}
	cancelled, e := m.Cancel(ctx, "alice", "job", now)
	if e != nil || cancelled.State != "cancelled" {
		t.Fatal(e)
	}
	if _, e = m.Update(ctx, "alice", "dev", "job", Update{Revision: running.Revision, State: "completed"}, now); !errors.Is(e, ErrConflict) {
		t.Fatal("resurrected cancellation")
	}
	if c, _ := m.Claim(ctx, "alice", "dev", now.Add(24*time.Hour)); c != nil {
		t.Fatal("replayed a claimed job")
	}
}
func TestTransitionsBoundedAndNoPublicCredential(t *testing.T) {
	now := time.Now().UTC()
	j := Job{State: "queued", Revision: 1, Expires: now.Add(time.Minute)}
	if _, e := Apply(j, Update{Revision: 1, State: "running"}, now); e == nil {
		t.Fatal("worker bypassed claim/approval")
	}
	j.State = "awaiting_approval"
	if _, e := Apply(j, Update{Revision: 1, State: "completed"}, now); e == nil {
		t.Fatal("completed before running")
	}
	j.State = "running"
	if _, e := Apply(j, Update{Revision: 1, State: "running", Text: strings.Repeat("x", (512<<10)+1)}, now); e == nil {
		t.Fatal("oversized text")
	}
	if _, e := Apply(j, Update{Revision: 1, State: "running", Thinking: strings.Repeat("x", MaxThinkingBytes+1)}, now); e == nil {
		t.Fatal("oversized thinking")
	}
	if _, e := Apply(j, Update{Revision: 1, State: "running", ThinkingMS: -1}, now); e == nil {
		t.Fatal("negative thinking time")
	}
	if next, e := Apply(j, Update{Revision: 1, State: "running", Thinking: "Looking", ThinkingMS: 7000}, now); e != nil || next.Thinking != "Looking" || next.ThinkingMS != 7000 {
		t.Fatalf("thinking not kept: %v %+v", e, next)
	}
	if _, e := Apply(j, Update{Revision: 1, State: "completed"}, now.Add(time.Hour)); e == nil {
		t.Fatal("expired write")
	}
	b, _ := json.Marshal(Device{ID: "d", Owner: "private-owner", KeyHash: "private-hash"})
	if strings.Contains(string(b), "private") {
		t.Fatal("credential exposed", string(b))
	}
}
func TestIdempotencyQueueAndCopy(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	now := time.Now().UTC()
	j := Job{Request: Request{ID: "one", DeviceID: "d", Prompt: "hello"}, Owner: "a", State: "queued", Created: now, Expires: now.Add(time.Minute)}
	if _, e := m.Create(ctx, j); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Create(ctx, j); e != nil {
		t.Fatal("idempotent create", e)
	}
	j.Prompt = "different"
	if _, e := m.Create(ctx, j); e == nil {
		t.Fatal("idempotency key reused with another payload")
	}
	j.ID = "overflow"
	if _, e := m.Create(ctx, j); e == nil {
		t.Fatal("queue unbounded")
	}
}
