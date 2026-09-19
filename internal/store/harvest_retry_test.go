package store

import "testing"

func TestHarvestErrorTransient(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"extract: openrouter 429: rate limit exceeded", true},
		{"openrouter throttle", true},
		{"context deadline exceeded: timeout", true},
		{"extract: openrouter 503: unavailable", true},
		{"invalid json from model", false},
		{"OPENROUTER_API_KEY unset", false},
	}
	for _, tc := range cases {
		if got := HarvestErrorTransient(tc.in); got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestMemHarvestRequeueAndClaim(t *testing.T) {
	q := NewMemHarvestQueue()
	job, created, err := q.EnqueueHarvestJob(t.Context(), "proj1", "test", []HarvestTurn{
		{Speaker: "user", Content: "remember we use postgres"},
	})
	if err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	claimed, err := q.ClaimNextHarvestJob(t.Context())
	if err != nil || claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	if err := q.RequeueHarvestJob(t.Context(), job.ID, "openrouter", "429 rate limit", HarvestRetryDelay(1)); err != nil {
		t.Fatal(err)
	}
	// Not ready yet (30s backoff).
	again, err := q.ClaimNextHarvestJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if again != nil {
		t.Fatalf("expected nil claim during backoff, got %s", again.ID)
	}
	got, err := q.GetHarvestJob(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != HarvestQueued || got.AttemptCount != 1 {
		t.Fatalf("after requeue: status=%s attempts=%d", got.Status, got.AttemptCount)
	}
}
