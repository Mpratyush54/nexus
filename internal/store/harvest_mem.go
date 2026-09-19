package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// MemHarvestQueue is an in-memory HarvestQueue for tests / local stub.
type MemHarvestQueue struct {
	mu   sync.Mutex
	jobs map[string]*HarvestJob
	order []string
}

func NewMemHarvestQueue() *MemHarvestQueue {
	return &MemHarvestQueue{jobs: map[string]*HarvestJob{}}
}

func (q *MemHarvestQueue) EnqueueHarvestJob(_ context.Context, projectID, source string, turns []HarvestTurn) (*HarvestJob, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cleaned := make([]HarvestTurn, 0, len(turns))
	for _, t := range turns {
		c := strings.TrimSpace(t.Content)
		if c == "" {
			continue
		}
		cleaned = append(cleaned, t)
	}
	if len(cleaned) == 0 {
		return nil, false, fmt.Errorf("harvest: no turns")
	}
	dedupe := HarvestDedupeKey(cleaned)
	for _, id := range q.order {
		j := q.jobs[id]
		if j != nil && j.ProjectID == projectID && j.DedupeKey == dedupe {
			cp := *j
			return &cp, false, nil
		}
	}
	now := time.Now().UTC()
	j := &HarvestJob{
		ID:         newID("hj"),
		ProjectID:  projectID,
		DedupeKey:  dedupe,
		Status:     HarvestQueued,
		Source:     source,
		Turns:      cleaned,
		RawPreview: HarvestRawPreview(cleaned, 600),
		TurnCount:  len(cleaned),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	q.jobs[j.ID] = j
	q.order = append(q.order, j.ID)
	cp := *j
	return &cp, true, nil
}

func (q *MemHarvestQueue) ClaimNextHarvestJob(_ context.Context) (*HarvestJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, id := range q.order {
		j := q.jobs[id]
		if j == nil || j.Status != HarvestQueued {
			continue
		}
		now := time.Now().UTC()
		j.Status = HarvestProcessing
		j.StartedAt = &now
		j.UpdatedAt = now
		cp := *j
		cp.Turns = append([]HarvestTurn(nil), j.Turns...)
		return &cp, nil
	}
	return nil, nil
}

func (q *MemHarvestQueue) FinishHarvestJob(_ context.Context, id, status, provider, errMsg string, resultCount int) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	j := q.jobs[id]
	if j == nil {
		return fmt.Errorf("harvest: not found")
	}
	now := time.Now().UTC()
	j.Status = status
	j.Provider = provider
	j.Error = errMsg
	j.ResultCount = resultCount
	j.FinishedAt = &now
	j.UpdatedAt = now
	return nil
}

func (q *MemHarvestQueue) ListHarvestJobs(_ context.Context, projectID string, limit int) ([]*HarvestJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if limit <= 0 {
		limit = 40
	}
	var out []*HarvestJob
	for i := len(q.order) - 1; i >= 0 && len(out) < limit; i-- {
		j := q.jobs[q.order[i]]
		if j == nil || j.ProjectID != projectID {
			continue
		}
		cp := *j
		cp.Turns = nil
		out = append(out, &cp)
	}
	return out, nil
}

func (q *MemHarvestQueue) GetHarvestJob(_ context.Context, id string) (*HarvestJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j := q.jobs[id]
	if j == nil {
		return nil, ErrNotFound
	}
	cp := *j
	return &cp, nil
}
