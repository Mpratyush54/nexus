package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Queue limits (phase-2 spec): at most 20 waiting reports, none older
// than 7 days.
const (
	MaxQueued     = 20
	MaxQueuedAge  = 7 * 24 * time.Hour
	retryBase     = 30 * time.Second
	retryMax      = time.Hour
	retryMissing  = 6 * time.Hour // server without /v1/diagnostics yet
	uploadTimeout = 20 * time.Second
)

// UploadResult is what happened to one queued report.
type UploadResult struct {
	ReportID  string    `json:"report_id"`
	Status    int       `json:"status,omitempty"`
	Sent      bool      `json:"sent"`     // the server has it (2xx, or a duplicate)
	Dropped   bool      `json:"dropped"`  // rejected for good (400/413) or expired
	Waiting   bool      `json:"waiting"`  // still queued; retried after RetryAt
	RetryAt   time.Time `json:"retry_at"` // zero when not waiting
	RequestID string    `json:"request_id,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Uploader sends pending reports to POST /v1/diagnostics with backoff.
// It keeps retry state in memory only: after a restart every pending
// report is due again, which is what "upload on the next start" means.
type Uploader struct {
	Base   string           // the folder holding reports/
	Server func() string    // API base URL, e.g. https://api-nexus.example
	Token  func() string    // saved sign-in for signed reports ("" = anonymous)
	Client *http.Client     // nil: a client with uploadTimeout
	Now    func() time.Time // nil: time.Now

	mu    sync.Mutex
	state map[string]*retryState
	busy  sync.Mutex
}

type retryState struct {
	attempts int
	next     time.Time
}

func (u *Uploader) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

// Pending lists queued report files, oldest first.
func (u *Uploader) Pending() []string {
	entries, err := os.ReadDir(PendingDir(u.Base))
	if err != nil {
		return nil
	}
	type item struct {
		path string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), plainExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(PendingDir(u.Base), e.Name()), info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.path
	}
	return out
}

// reportIDFromFile returns the ID and whether the file is signed.
func reportIDFromFile(path string) (string, bool) {
	name := filepath.Base(path)
	if strings.HasSuffix(name, signedExt) {
		return strings.TrimSuffix(name, signedExt), true
	}
	return strings.TrimSuffix(name, plainExt), false
}

// Prune drops held and pending reports older than MaxQueuedAge and keeps
// at most MaxQueued of each, newest kept.
func Prune(base string, now time.Time) {
	for _, dir := range []string{HeldDir(base), PendingDir(base)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		type item struct {
			path string
			mod  time.Time
		}
		var items []item
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			path := filepath.Join(dir, e.Name())
			info, err := e.Info()
			if err != nil {
				continue
			}
			if strings.HasSuffix(e.Name(), ".tmp") && now.Sub(info.ModTime()) > time.Hour {
				_ = os.Remove(path)
				continue
			}
			if !strings.HasSuffix(e.Name(), plainExt) {
				continue
			}
			if now.Sub(info.ModTime()) > MaxQueuedAge {
				_ = os.Remove(path)
				continue
			}
			items = append(items, item{path, info.ModTime()})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
		for i := MaxQueued; i < len(items); i++ {
			_ = os.Remove(items[i].path)
		}
	}
}

// Flush uploads every due pending report (or only the one with reportID,
// when it is set; force ignores its backoff, for an explicit Send). Calls
// are serialised so two flushes never post the same file twice at once.
func (u *Uploader) Flush(ctx context.Context, reportID string, force bool) []UploadResult {
	u.busy.Lock()
	defer u.busy.Unlock()
	Prune(u.Base, u.now())
	var results []UploadResult
	for _, path := range u.Pending() {
		id, signed := reportIDFromFile(path)
		if reportID != "" && id != reportID {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		u.mu.Lock()
		st := u.stateFor(id)
		due := force || !u.now().Before(st.next)
		next := st.next
		u.mu.Unlock()
		if !due {
			results = append(results, UploadResult{ReportID: id, Waiting: true, RetryAt: next})
			continue
		}
		results = append(results, u.upload(ctx, path, id, signed))
	}
	return results
}

func (u *Uploader) stateFor(id string) *retryState {
	if u.state == nil {
		u.state = map[string]*retryState{}
	}
	st := u.state[id]
	if st == nil {
		st = &retryState{}
		u.state[id] = st
	}
	return st
}

func (u *Uploader) upload(ctx context.Context, path, id string, signed bool) UploadResult {
	res := UploadResult{ReportID: id}
	body, err := os.ReadFile(path)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	server := ""
	if u.Server != nil {
		server = strings.TrimRight(strings.TrimSpace(u.Server()), "/")
	}
	if server == "" {
		return u.retry(res, 0, "no server configured")
	}
	if len(body) > MaxBodyBytes {
		// Never accepted; shrink it once instead of dropping the report.
		var r Report
		if json.Unmarshal(body, &r) == nil {
			r.Logs = r.Logs[max(0, len(r.Logs)-50):]
			r.Stack = truncate(r.Stack, 8<<10)
			body, _ = json.Marshal(r)
		}
	}
	reqCtx, cancel := context.WithTimeout(ctx, uploadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, server+"/v1/diagnostics", bytes.NewReader(body))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	if signed && u.Token != nil {
		if token := strings.TrimSpace(u.Token()); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	client := u.Client
	if client == nil {
		client = &http.Client{Timeout: uploadTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return u.retry(res, 0, Redact(err.Error()))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	res.Status = resp.StatusCode
	res.RequestID = ResponseRequestID(resp.Header, raw)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.Sent = true
		u.forget(id)
		_ = os.Remove(path)
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusRequestEntityTooLarge:
		res.Dropped = true
		res.Error = ServerErrorMessage(raw, resp.Status)
		u.forget(id)
		_ = os.Remove(path)
	default:
		wait := retryAfter(resp.Header.Get("Retry-After"), u.now())
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			wait = max(wait, retryMissing)
		}
		res = u.retry(res, wait, ServerErrorMessage(raw, resp.Status))
	}
	return res
}

// retry schedules the next attempt: exponential from 30s, capped at 1h,
// and never sooner than the server's Retry-After (minWait).
func (u *Uploader) retry(res UploadResult, minWait time.Duration, msg string) UploadResult {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := u.stateFor(res.ReportID)
	st.attempts++
	wait := Backoff(st.attempts)
	if minWait > wait {
		wait = minWait
	}
	st.next = u.now().Add(wait)
	res.Waiting, res.RetryAt, res.Error = true, st.next, msg
	return res
}

func (u *Uploader) forget(id string) {
	u.mu.Lock()
	delete(u.state, id)
	u.mu.Unlock()
}

// Backoff is the wait before attempt n+1 after n failures: 30s, 1m, 2m, …
// capped at an hour.
func Backoff(attempts int) time.Duration {
	if attempts <= 0 {
		return 0
	}
	d := retryBase
	for i := 1; i < attempts && d < retryMax; i++ {
		d *= 2
	}
	if d > retryMax {
		d = retryMax
	}
	return d
}

func retryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil && secs > 0 {
		return min(time.Duration(secs)*time.Second, 24*time.Hour)
	}
	if at, err := http.ParseTime(header); err == nil && at.After(now) {
		return min(at.Sub(now), 24*time.Hour)
	}
	return 0
}

// ResponseRequestID returns the server's request ID for a response: the
// X-Request-ID header, else error.request_id or request_id in the body.
func ResponseRequestID(h http.Header, body []byte) string {
	if id := strings.TrimSpace(h.Get("X-Request-ID")); id != "" && reportIDPattern.MatchString(id) {
		return id
	}
	var parsed struct {
		RequestID string          `json:"request_id"`
		Error     json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	if len(parsed.Error) > 0 {
		var inner struct {
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(parsed.Error, &inner) == nil && inner.RequestID != "" {
			return inner.RequestID
		}
	}
	return parsed.RequestID
}

// ServerErrorMessage pulls error.message (or a string error) out of an
// API error body, falling back to the HTTP status.
func ServerErrorMessage(body []byte, status string) string {
	var parsed struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && len(parsed.Error) > 0 {
		var inner struct {
			Message string `json:"message"`
		}
		var plain string
		if json.Unmarshal(parsed.Error, &inner) == nil && inner.Message != "" {
			return inner.Message
		}
		if json.Unmarshal(parsed.Error, &plain) == nil && plain != "" {
			return plain
		}
	}
	return status
}
