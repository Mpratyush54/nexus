package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
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
	SessionID string `json:"session_id,omitempty"`
}

// Harvest retry policy for transient provider failures (429, 5xx, timeout).
const (
	HarvestMaxAttempts = 8
)

// HarvestJob is a queued batch of turns visible in the portal while the
// OpenRouter worker processes it.
type HarvestJob struct {
	ID            string        `json:"id"`
	ProjectID     string        `json:"project_id"`
	DedupeKey     string        `json:"dedupe_key"`
	Status        string        `json:"status"`
	Source        string        `json:"source,omitempty"`
	Turns         []HarvestTurn `json:"turns,omitempty"`
	RawPreview    string        `json:"raw_preview"`
	TurnCount     int           `json:"turn_count"`
	ResultCount   int           `json:"result_count"`
	Error         string        `json:"error,omitempty"`
	Provider      string        `json:"provider,omitempty"`
	AttemptCount  int           `json:"attempt_count"`
	NextAttemptAt *time.Time    `json:"next_attempt_at,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	StartedAt     *time.Time    `json:"started_at,omitempty"`
	FinishedAt    *time.Time    `json:"finished_at,omitempty"`
}

// SanitizeUTF8 strips nulls and invalid UTF-8 so Postgres TEXT/JSONB inserts
// never fail with SQLSTATE 22021. Prefer DecodeRune over ToValidUTF8 alone so
// overlong / broken lead bytes (e.g. 0xe2 0xe2 0x80) cannot survive.
func SanitizeUTF8(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\x00", "")
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

// TruncateUTF8 caps byte length without splitting a multi-byte rune.
func TruncateUTF8(s string, max int) string {
	s = SanitizeUTF8(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// HarvestErrorTransient reports whether err text looks retryable (rate limits,
// timeouts, upstream 5xx). Permanent extract/parse failures return false.
func HarvestErrorTransient(errMsg string) bool {
	e := strings.ToLower(errMsg)
	if e == "" {
		return false
	}
	needles := []string{
		"429", "rate limit", "throttle", "timeout", "timed out",
		"502", "503", "504", "529", "overloaded", "temporarily",
		"connection reset", "eof", "unavailable",
	}
	for _, n := range needles {
		if strings.Contains(e, n) {
			return true
		}
	}
	return false
}

// HarvestRetryDelay returns backoff after the given 1-based attempt number.
func HarvestRetryDelay(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 30 * time.Second
	case attempt == 2:
		return 1 * time.Minute
	case attempt == 3:
		return 2 * time.Minute
	case attempt == 4:
		return 5 * time.Minute
	case attempt == 5:
		return 10 * time.Minute
	case attempt == 6:
		return 20 * time.Minute
	default:
		return 30 * time.Minute
	}
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
		c := strings.TrimSpace(SanitizeUTF8(t.Content))
		if c == "" {
			continue
		}
		sp := strings.TrimSpace(SanitizeUTF8(t.Speaker))
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
				b.WriteString(TruncateUTF8(line, remain))
				b.WriteString("…")
			}
			break
		}
		b.WriteString(line)
	}
	return SanitizeUTF8(b.String())
}

// CleanHarvestTurns normalizes speaker/content for Postgres-safe enqueue.
func CleanHarvestTurns(turns []HarvestTurn) []HarvestTurn {
	cleaned := make([]HarvestTurn, 0, len(turns))
	for _, t := range turns {
		c := strings.TrimSpace(SanitizeUTF8(t.Content))
		if c == "" {
			continue
		}
		c = TruncateUTF8(c, 8000)
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		cleaned = append(cleaned, HarvestTurn{
			Speaker:   TruncateUTF8(SanitizeUTF8(strings.TrimSpace(t.Speaker)), 200),
			Content:   c,
			Timestamp: TruncateUTF8(SanitizeUTF8(strings.TrimSpace(t.Timestamp)), 64),
			SessionID: TruncateUTF8(SanitizeUTF8(strings.TrimSpace(t.SessionID)), 200),
		})
	}
	return cleaned
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
	// RequeueHarvestJob marks a job queued again after a transient failure.
	// attempt becomes previous+1; next run waits until now+delay.
	RequeueHarvestJob(ctx context.Context, id, provider, errMsg string, delay time.Duration) error
	ListHarvestJobs(ctx context.Context, projectID string, limit int) ([]*HarvestJob, error)
	ListHarvestJobsOpt(ctx context.Context, projectID string, limit int, includeTurns bool) ([]*HarvestJob, error)
	GetHarvestJob(ctx context.Context, id string) (*HarvestJob, error)
}
