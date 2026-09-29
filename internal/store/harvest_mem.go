package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemHarvestQueue is an in-memory HarvestQueue for tests / local stub.
type MemHarvestQueue struct {
	mu    sync.Mutex
	jobs  map[string]*HarvestJob
	order []string
}

func NewMemHarvestQueue() *MemHarvestQueue {
	return &MemHarvestQueue{jobs: map[string]*HarvestJob{}}
}

func (q *MemHarvestQueue) EnqueueHarvestJob(_ context.Context, projectID, source string, turns []HarvestTurn) (*HarvestJob, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cleaned := CleanHarvestTurns(turns)
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
		Source:     SanitizeUTF8(strings.TrimSpace(source)),
		Turns:      cleaned,
		RawPreview: HarvestRawPreview(cleaned, 8000),
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
	now := time.Now().UTC()
	// Reclaim stuck processing + transient failed (mirrors Postgres).
	for _, id := range q.order {
		j := q.jobs[id]
		if j == nil {
			continue
		}
		if j.Status == HarvestProcessing && j.StartedAt != nil && now.Sub(*j.StartedAt) > 3*time.Minute {
			j.Status = HarvestQueued
			j.StartedAt = nil
			j.Error = "requeued: processing timed out"
			j.UpdatedAt = now
			at := now
			j.NextAttemptAt = &at
		}
		if j.Status == HarvestFailed && j.AttemptCount < HarvestMaxAttempts &&
			HarvestErrorTransient(j.Error) &&
			j.FinishedAt != nil && now.Sub(*j.FinishedAt) < 48*time.Hour {
			j.Status = HarvestQueued
			j.StartedAt = nil
			j.FinishedAt = nil
			if j.AttemptCount < 1 {
				j.AttemptCount = 1
			}
			at := now
			j.NextAttemptAt = &at
			j.UpdatedAt = now
		}
	}
	for _, id := range q.order {
		j := q.jobs[id]
		if j == nil || j.Status != HarvestQueued {
			continue
		}
		if j.NextAttemptAt != nil && j.NextAttemptAt.After(now) {
			continue
		}
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
	j.NextAttemptAt = nil
	return nil
}

func (q *MemHarvestQueue) RequeueHarvestJob(_ context.Context, id, provider, errMsg string, delay time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	j := q.jobs[id]
	if j == nil {
		return fmt.Errorf("harvest: not found")
	}
	now := time.Now().UTC()
	j.Status = HarvestQueued
	j.Provider = provider
	j.Error = errMsg
	j.AttemptCount++
	next := now.Add(delay)
	j.NextAttemptAt = &next
	j.StartedAt = nil
	j.FinishedAt = nil
	j.UpdatedAt = now
	return nil
}

func (q *MemHarvestQueue) ListHarvestJobs(ctx context.Context, projectID string, limit int) ([]*HarvestJob, error) {
	return q.ListHarvestJobsOpt(ctx, projectID, limit, false)
}

func (q *MemHarvestQueue) ListHarvestJobsOpt(_ context.Context, projectID string, limit int, includeTurns bool) ([]*HarvestJob, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if limit <= 0 {
		limit = 40
	}
	var matched []*HarvestJob
	for i := len(q.order) - 1; i >= 0; i-- {
		j := q.jobs[q.order[i]]
		if j == nil || j.ProjectID != projectID {
			continue
		}
		cp := *j
		if !includeTurns {
			cp.Turns = nil
		} else {
			cp.Turns = append([]HarvestTurn(nil), j.Turns...)
		}
		matched = append(matched, &cp)
	}
	// Prefer in-flight jobs (same priority as Postgres list).
	rank := func(status string) int {
		switch status {
		case HarvestProcessing:
			return 0
		case HarvestQueued:
			return 1
		case HarvestFailed:
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		ri, rj := rank(matched[i].Status), rank(matched[j].Status)
		if ri != rj {
			return ri < rj
		}
		return matched[i].CreatedAt.After(matched[j].CreatedAt)
	})
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

func (q *MemHarvestQueue) CountHarvestJobs(_ context.Context, projectID string) (HarvestJobCounts, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var c HarvestJobCounts
	for _, id := range q.order {
		j := q.jobs[id]
		if j == nil || j.ProjectID != projectID {
			continue
		}
		switch j.Status {
		case HarvestQueued:
			c.Queued++
		case HarvestProcessing:
			c.Processing++
		case HarvestDone:
			c.Done++
		case HarvestFailed:
			c.Failed++
		case HarvestDuplicate:
			c.Duplicate++
		}
		c.Total++
	}
	return c, nil
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
