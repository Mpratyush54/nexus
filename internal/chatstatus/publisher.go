// Package chatstatus publishes what the desktop's agents are doing in each
// chat (internal/agentrun's live.status) to the server, so the web chat list
// shows the same status dots as the desktop sidebar: running, waiting for
// approval, a reply not yet read, stopped with a problem. It also brings
// back the chats read on the web, so the desktop clears their unread mark.
//
// It is light by design: changes are sent together after a short quiet
// spell (Debounce), working and unread chats are repeated every Keepalive
// so the server can tell a closed desktop from a long turn, and a server
// without the endpoint is left alone for a while.
package chatstatus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Report is one chat's status, as PUT /v1/chat-status takes it.
type Report struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Unread    bool   `json:"unread"`
}

// Doer sends one request to the server (core.Cloud).
type Doer interface {
	Do(ctx context.Context, method, path string, body []byte) ([]byte, error)
}

// StatusCoder is an error that carries an HTTP status (core.CloudError).
type StatusCoder interface{ HTTPStatus() int }

const (
	DefaultDebounce  = 1500 * time.Millisecond
	DefaultKeepalive = time.Minute
	// unsupportedPause is how long a server without the endpoint is left alone.
	unsupportedPause = 30 * time.Minute
	maxPending       = 500
	maxReport        = 200
)

// Publisher sends status changes. The zero value is not usable; set Cloud.
type Publisher struct {
	// Cloud returns the signed-in server, or nil when signed out.
	Cloud func() Doer
	// List returns every chat with a status worth showing (working or
	// unread), for the keepalive. Optional.
	List func() []Report
	// Read clears the desktop's unread mark of a chat read on the web.
	// Optional. Called from the publisher's goroutine.
	Read func(sessionID string)
	// Debounce and Keepalive default to DefaultDebounce and DefaultKeepalive.
	Debounce  time.Duration
	Keepalive time.Duration
	// Now is for tests.
	Now func() time.Time

	mu         sync.Mutex
	pending    map[string]Report
	sent       map[string]Report
	wake       chan struct{}
	pausedTill time.Time
}

// Note takes one core event; only live.status events matter. It never
// blocks, so it can be called with the session manager's lock held.
func (p *Publisher) Note(event map[string]any) {
	if t, _ := event["type"].(string); t != "live.status" {
		return
	}
	id, _ := event["session_id"].(string)
	status, _ := event["status"].(string)
	unread, _ := event["unread"].(bool)
	r := Report{SessionID: strings.TrimSpace(id), Status: status, Unread: unread}
	if r.SessionID == "" || len(r.SessionID) > 128 || !knownStatus(r.Status) {
		return
	}
	p.mu.Lock()
	if p.pending == nil {
		p.pending = map[string]Report{}
	}
	if len(p.pending) < maxPending || p.pending[r.SessionID] != (Report{}) {
		p.pending[r.SessionID] = r
	}
	wake := p.wakeLocked()
	p.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (p *Publisher) wakeLocked() chan struct{} {
	if p.wake == nil {
		p.wake = make(chan struct{}, 1)
	}
	return p.wake
}

func knownStatus(s string) bool {
	switch s {
	case "running", "approval", "queued", "done", "error", "interrupted", "idle":
		return true
	}
	return false
}

// worthRepeating reports whether the keepalive repeats a status.
func worthRepeating(r Report) bool {
	return r.Status == "running" || r.Status == "approval" || r.Status == "queued" || r.Status == "interrupted" || r.Unread
}

// Run sends until ctx ends.
func (p *Publisher) Run(ctx context.Context) {
	debounce, keepalive := p.Debounce, p.Keepalive
	if debounce <= 0 {
		debounce = DefaultDebounce
	}
	if keepalive <= 0 {
		keepalive = DefaultKeepalive
	}
	p.mu.Lock()
	wake := p.wakeLocked()
	p.mu.Unlock()
	tick := time.NewTicker(keepalive)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			// Let a burst of changes settle, then send them together.
			select {
			case <-ctx.Done():
				return
			case <-time.After(debounce):
			}
			p.Flush(ctx, false)
		case <-tick.C:
			p.Flush(ctx, true)
		}
	}
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Flush sends the pending changes, and with keepalive also every working
// or unread chat. It returns what it sent (for tests).
func (p *Publisher) Flush(ctx context.Context, keepalive bool) []Report {
	p.mu.Lock()
	if p.now().Before(p.pausedTill) {
		p.mu.Unlock()
		return nil
	}
	batch := map[string]Report{}
	for id, r := range p.pending {
		batch[id] = r
	}
	p.pending = map[string]Report{}
	sent := make(map[string]Report, len(p.sent))
	for id, r := range p.sent {
		sent[id] = r
	}
	p.mu.Unlock()
	if keepalive && p.List != nil {
		listed := map[string]bool{}
		for _, r := range p.List() {
			listed[r.SessionID] = true
			if _, changed := batch[r.SessionID]; !changed && knownStatus(r.Status) && worthRepeating(r) {
				batch[r.SessionID] = r
			}
		}
		// A chat that went quiet since the last report is said to be idle,
		// so the web drops its dot even if its live.status was missed.
		for id, r := range sent {
			if _, ok := batch[id]; !ok && !listed[id] && worthRepeating(r) {
				batch[id] = Report{SessionID: id, Status: "idle"}
			}
		}
	}
	if len(batch) == 0 {
		return nil
	}
	items := make([]Report, 0, len(batch))
	for _, r := range batch {
		items = append(items, r)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].SessionID < items[j].SessionID })
	var cloud Doer
	if p.Cloud != nil {
		cloud = p.Cloud()
	}
	if cloud == nil {
		p.requeue(items)
		return nil
	}
	var read []string
	for start := 0; start < len(items); start += maxReport {
		chunk := items[start:min(start+maxReport, len(items))]
		got, err := p.send(ctx, cloud, chunk)
		if err != nil {
			p.requeue(items[start:])
			return items[:start]
		}
		read = append(read, got...)
		p.mu.Lock()
		if p.sent == nil {
			p.sent = map[string]Report{}
		}
		for _, r := range chunk {
			if worthRepeating(r) {
				p.sent[r.SessionID] = r
			} else {
				delete(p.sent, r.SessionID)
			}
		}
		p.mu.Unlock()
	}
	if p.Read != nil {
		for _, id := range read {
			p.Read(id)
		}
	}
	return items
}

func (p *Publisher) send(ctx context.Context, cloud Doer, items []Report) ([]string, error) {
	body, _ := json.Marshal(map[string]any{"items": items})
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := cloud.Do(callCtx, http.MethodPut, "/v1/chat-status", body)
	if err != nil {
		var coded StatusCoder
		if errors.As(err, &coded) {
			switch coded.HTTPStatus() {
			case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
				// An older server: try again much later, quietly.
				p.mu.Lock()
				p.pausedTill = p.now().Add(unsupportedPause)
				p.mu.Unlock()
			case http.StatusBadRequest:
				// Never retry a report the server refuses.
				return nil, nil
			}
		}
		return nil, err
	}
	var out struct {
		Read []string `json:"read"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.Read, nil
}

// requeue keeps unsent changes for the next try; a newer change wins.
func (p *Publisher) requeue(items []Report) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil {
		p.pending = map[string]Report{}
	}
	for _, r := range items {
		if _, newer := p.pending[r.SessionID]; !newer && len(p.pending) < maxPending {
			p.pending[r.SessionID] = r
		}
	}
}
