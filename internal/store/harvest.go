package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Harvest job statuses (queue → OpenRouter worker).
const (
	HarvestQueued     = "queued"
	HarvestProcessing = "processing"
	HarvestDone       = "done"
	HarvestFailed     = "failed"
	HarvestDuplicate  = "duplicate"
)

// HarvestTurn is one raw conversation line uploaded by the daemon.
type HarvestTurn struct {
	Speaker   string `json:"speaker"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp,omitempty"`
}

// HarvestJob is a queued batch of turns visible in the portal while the
// OpenRouter worker processes it.
type HarvestJob struct {
	ID          string        `json:"id"`
	ProjectID   string        `json:"project_id"`
	DedupeKey   string        `json:"dedupe_key"`
	Status      string        `json:"status"`
	Source      string        `json:"source,omitempty"`
	Turns       []HarvestTurn `json:"turns,omitempty"`
	RawPreview  string        `json:"raw_preview"`
	TurnCount   int           `json:"turn_count"`
	ResultCount int           `json:"result_count"`
	Error       string        `json:"error,omitempty"`
	Provider    string        `json:"provider,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	StartedAt   *time.Time    `json:"started_at,omitempty"`
	FinishedAt  *time.Time    `json:"finished_at,omitempty"`
}

// HarvestDedupeKey hashes normalized turn text for idempotent enqueue.
func HarvestDedupeKey(turns []HarvestTurn) string {
	h := sha256.New()
	for _, t := range turns {
		sp := strings.ToLower(strings.TrimSpace(t.Speaker))
		c := strings.TrimSpace(t.Content)
		if c == "" {
			continue
		}
		_, _ = h.Write([]byte(sp))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(c))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HarvestRawPreview builds a short human-readable snippet for the UI.
func HarvestRawPreview(turns []HarvestTurn, max int) string {
	if max <= 0 {
		max = 500
	}
	var b strings.Builder
	for _, t := range turns {
		c := strings.TrimSpace(t.Content)
		if c == "" {
			continue
		}
		sp := strings.TrimSpace(t.Speaker)
		if sp == "" {
			sp = "?"
		}
		line := sp + ": " + c
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		if b.Len()+len(line) > max {
			remain := max - b.Len() - 1
			if remain > 20 {
				b.WriteString(line[:remain])
				b.WriteString("…")
			}
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// MarshalHarvestTurns encodes turns for JSONB storage.
func MarshalHarvestTurns(turns []HarvestTurn) ([]byte, error) {
	if turns == nil {
		turns = []HarvestTurn{}
	}
	return json.Marshal(turns)
}

// HarvestQueue persists and claims harvest jobs.
type HarvestQueue interface {
	EnqueueHarvestJob(ctx context.Context, projectID, source string, turns []HarvestTurn) (job *HarvestJob, created bool, err error)
	ClaimNextHarvestJob(ctx context.Context) (*HarvestJob, error)
	FinishHarvestJob(ctx context.Context, id, status, provider, errMsg string, resultCount int) error
	ListHarvestJobs(ctx context.Context, projectID string, limit int) ([]*HarvestJob, error)
	GetHarvestJob(ctx context.Context, id string) (*HarvestJob, error)
}
