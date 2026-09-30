package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CloudClient is the thin /v1 client Drain uses (same shape as core.Cloud).
type CloudClient interface {
	Do(ctx context.Context, method, path string, body []byte) ([]byte, error)
}

// Envelope is the JSON shape stored in Item.Body for drainable capture kinds.
// Harvest should Enqueue envelopes (via CaptureEnqueue) rather than live-push.
type Envelope struct {
	Method string          `json:"method,omitempty"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// DrainResult summarizes one Drain pass.
type DrainResult struct {
	Uploaded int `json:"uploaded"`
	Pending  int `json:"pending"`
}

// CaptureEnqueue records a capture for later upload. Prefer this over live-push
// so offline capture survives until uploads.retry / Drain can Ack.
func CaptureEnqueue(s *Spool, id, kind, method, path string, body []byte) error {
	if s == nil {
		return errors.New("outbox: spool is required")
	}
	kind = strings.TrimSpace(kind)
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("outbox: path is required")
	}
	method = strings.TrimSpace(method)
	if method == "" {
		method = defaultMethod(kind)
	}
	env := Envelope{Method: method, Path: path}
	if len(body) > 0 {
		env.Body = append(json.RawMessage(nil), body...)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return s.Enqueue(id, kind, raw)
}

// Drain uploads pending items in kind order (turn, blob, manifest) and Acks
// each successful upload. Stops on the first failure so a later retry can
// resume with zero loss; already-Acked items stay Acked.
func Drain(ctx context.Context, s *Spool, cloud CloudClient) (DrainResult, error) {
	var res DrainResult
	if s == nil {
		return res, errors.New("outbox: spool is required")
	}
	if cloud == nil {
		return res, errors.New("outbox: cloud client is required")
	}
	pending := s.Pending()
	sort.SliceStable(pending, func(i, j int) bool {
		return kindRank(pending[i].Kind) < kindRank(pending[j].Kind)
	})
	for _, it := range pending {
		if err := ctx.Err(); err != nil {
			res.Pending = len(s.Pending())
			return res, err
		}
		method, path, body, err := resolveUpload(it)
		if err != nil {
			_ = s.BumpAttempt(it.ID)
			res.Pending = len(s.Pending())
			return res, err
		}
		if _, err := cloud.Do(ctx, method, path, body); err != nil {
			_ = s.BumpAttempt(it.ID)
			res.Pending = len(s.Pending())
			return res, fmt.Errorf("outbox: upload %s (%s): %w", it.ID, it.Kind, err)
		}
		if err := s.Ack(it.ID); err != nil {
			res.Pending = len(s.Pending())
			return res, err
		}
		res.Uploaded++
	}
	res.Pending = len(s.Pending())
	return res, nil
}

func resolveUpload(it Item) (method, path string, body []byte, err error) {
	var env Envelope
	if err := json.Unmarshal(it.Body, &env); err != nil {
		return "", "", nil, fmt.Errorf("outbox: item %s: envelope: %w", it.ID, err)
	}
	path = strings.TrimSpace(env.Path)
	if path == "" {
		return "", "", nil, fmt.Errorf("outbox: item %s: path is required", it.ID)
	}
	method = strings.TrimSpace(env.Method)
	if method == "" {
		method = defaultMethod(it.Kind)
	}
	body = []byte(env.Body)
	if len(body) == 0 {
		body = nil
	}
	return method, path, body, nil
}

func defaultMethod(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "blob":
		return "PUT"
	default:
		return "POST"
	}
}

func kindRank(kind string) int {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "turn":
		return 0
	case "blob":
		return 1
	case "manifest":
		return 2
	default:
		return 50
	}
}
