package store

import (
	"fmt"
	"testing"
)

func TestMemHarvestCountExceedsListLimit(t *testing.T) {
	q := NewMemHarvestQueue()
	const project = "proj-counts"
	// Enqueue more than the default list page so len(items) would falsely report 40.
	for i := 0; i < 55; i++ {
		_, created, err := q.EnqueueHarvestJob(t.Context(), project, "test", []HarvestTurn{
			{Speaker: "user", Content: fmt.Sprintf("decision batch %d unique content for harvest counts", i)},
		})
		if err != nil || !created {
			t.Fatalf("enqueue %d: created=%v err=%v", i, created, err)
		}
	}
	// Mark a few as done / processing so counts are multi-status.
	listed, err := q.ListHarvestJobs(t.Context(), project, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) < 55 {
		t.Fatalf("listed = %d want 55", len(listed))
	}
	_ = q.FinishHarvestJob(t.Context(), listed[0].ID, HarvestDone, "openrouter", "", 1)
	_ = q.FinishHarvestJob(t.Context(), listed[1].ID, HarvestFailed, "openrouter", "boom", 0)
	claimed, err := q.ClaimNextHarvestJob(t.Context())
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}

	page, err := q.ListHarvestJobs(t.Context(), project, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 40 {
		t.Fatalf("page len = %d want 40", len(page))
	}
	// In-flight should lead the capped page.
	if page[0].Status != HarvestProcessing && page[0].Status != HarvestQueued {
		t.Fatalf("first page status = %s want in-flight", page[0].Status)
	}

	counts, err := q.CountHarvestJobs(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Total != 55 {
		t.Fatalf("total = %d want 55", counts.Total)
	}
	if counts.Done != 1 || counts.Failed != 1 || counts.Processing != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	wantQueued := 55 - 3
	if counts.Queued != wantQueued {
		t.Fatalf("queued = %d want %d", counts.Queued, wantQueued)
	}
	if counts.InFlight() != wantQueued+1 {
		t.Fatalf("in_flight = %d", counts.InFlight())
	}
	if counts.Total == len(page) {
		t.Fatal("total must not equal capped page length")
	}
}
