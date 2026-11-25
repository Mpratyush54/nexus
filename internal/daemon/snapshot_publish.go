package daemon

// Snapshot publishing: failure backoff, upload timeouts and the worker.
//
// In real use a failing upload used to be retried on every snapshot cycle:
// each retry re-read, secret-scanned and gzipped the whole transcript (or
// queried the OpenCode database) only to fail the same way, for dozens of
// sessions. Every session now tracks each publishing step and backs off
// after a failure, and the expensive collection runs only when some step is
// due. Uploads run on their own worker goroutine and HTTP connection pool,
// so a slow upload never holds up the capture loop or the heartbeat.

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// snapshotBackoffSteps is how long a session's failed step waits before the
// next attempt: 5 m, 15 m, 1 h, then every 6 h. A success resets it.
var snapshotBackoffSteps = []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour}

func snapshotBackoff(failures int) time.Duration {
	if failures < 1 {
		return 0
	}
	if failures > len(snapshotBackoffSteps) {
		failures = len(snapshotBackoffSteps)
	}
	return snapshotBackoffSteps[failures-1]
}

// publishTrack is one publishing step's state for one session.
type publishTrack struct {
	failures int
	retryAt  time.Time
	// badStamp is content the server refused for good (a 4xx): it is not
	// sent again until the transcript changes.
	badStamp string
	// badSize: the server refused this size as too large (413); a transcript
	// this size or larger is not sent again.
	badSize int64
}

// due reports whether the step may run now for this content.
func (t *publishTrack) due(now time.Time, stamp string, size int64) bool {
	if t == nil {
		return true
	}
	if now.Before(t.retryAt) {
		return false
	}
	if t.badStamp != "" && t.badStamp == stamp {
		return false
	}
	if t.badSize > 0 && size >= t.badSize {
		return false
	}
	return true
}

// fail records a failure and reports whether it was permanent and newly so
// (callers log a permanent refusal once).
func (t *publishTrack) fail(now time.Time, err error, stamp string, size int64) (permanent bool) {
	t.failures++
	t.retryAt = now.Add(snapshotBackoff(t.failures))
	permanent, tooLarge := classifyUploadError(err)
	switch {
	case tooLarge:
		t.badSize = size
	case permanent:
		t.badStamp = stamp
	}
	return permanent || tooLarge
}

// MaxUploadGzipBytes caps one session upload (the gzip transcript): the
// server assembles a blob in memory and stores it as one row.
const MaxUploadGzipBytes = 8 << 20

// markTooLarge stops uploads of this transcript size or larger (or, with
// no file size to go by, of this content).
func (t *publishTrack) markTooLarge(size int64, stamp string) {
	if size > 0 {
		t.badSize = size
		return
	}
	t.badStamp = stamp
}

func (t *publishTrack) succeed() {
	*t = publishTrack{}
}

// classifyUploadError sorts a failed upload: tooLarge for 413, permanent for
// other client errors about the request itself (it will fail again as is).
// Authentication, rate limits, server errors and network failures are
// transient (backed off, retried).
func classifyUploadError(err error) (permanent, tooLarge bool) {
	var se *mirrorStatusError
	if !errors.As(err, &se) {
		if strings.Contains(err.Error(), "no turns extracted") {
			return true, false // nothing to publish for this content
		}
		return false, false
	}
	switch se.status {
	case http.StatusRequestEntityTooLarge:
		return false, true
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusConflict,
		http.StatusGone, http.StatusUnprocessableEntity, http.StatusPaymentRequired:
		return true, false
	}
	return false, false
}

// snapshotTracks holds a runtime's per-session publishing state.
type snapshotTracks struct {
	collect, version, legacy map[string]*publishTrack
	// published: sessions that have a cloud version; their legacy snapshot
	// is no longer pushed.
	published map[string]bool
}

func (s *snapshotTracks) get(m *map[string]*publishTrack, sid string) *publishTrack {
	if *m == nil {
		*m = map[string]*publishTrack{}
	}
	t := (*m)[sid]
	if t == nil {
		t = &publishTrack{}
		(*m)[sid] = t
	}
	return t
}

// --- upload transport ---

// Uploads have their own connection pool: on one shared HTTP/2 connection a
// multi-megabyte upload delayed the heartbeat and API calls past their
// 15-20 s timeouts. They also get time in proportion to their size instead
// of the API client's fixed 20 s, which a slow uplink cannot meet.
var (
	uploadTransportOnce sync.Once
	uploadTransport     http.RoundTripper
)

func sharedUploadTransport() http.RoundTripper {
	uploadTransportOnce.Do(func() {
		if t, ok := http.DefaultTransport.(*http.Transport); ok {
			c := t.Clone()
			c.MaxIdleConnsPerHost = 2
			uploadTransport = c
			return
		}
		uploadTransport = &http.Transport{Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext}
	})
	return uploadTransport
}

// uploadClient is the client for transcript uploads (no overall timeout:
// each request gets uploadTimeout). Tests that set HTTP.Transport keep it.
func (s *HTTPMemoryStore) uploadClient() *http.Client {
	if s.HTTP != nil && s.HTTP.Transport != nil {
		return &http.Client{Transport: s.HTTP.Transport}
	}
	return &http.Client{Transport: sharedUploadTransport()}
}

// Upload deadlines: a base plus the body at a slow uplink's rate.
var (
	uploadBaseTimeout  = time.Minute
	uploadMinBytesPerS = int64(64 << 10)
	assembleTimeout    = 5 * time.Minute
)

func uploadTimeout(length int64) time.Duration {
	return uploadBaseTimeout + time.Duration(length/uploadMinBytesPerS)*time.Second
}

// --- worker ---

type snapshotRequest struct {
	sid   string
	force bool
}

// queueSnapshot asks the snapshot worker to consider sid. Requests for a
// session already queued merge (force wins); the capture loop never waits.
func (r *Runtime) queueSnapshot(sid string, force bool) {
	r.snapQueueMu.Lock()
	if r.snapQueued == nil {
		r.snapQueued = map[string]bool{}
	}
	prev, queued := r.snapQueued[sid]
	r.snapQueued[sid] = prev || force
	ch := r.snapQueue
	r.snapQueueMu.Unlock()
	if queued || ch == nil {
		return
	}
	select {
	case ch <- sid:
	default:
		// Full: the periodic pass will ask again.
		r.snapQueueMu.Lock()
		delete(r.snapQueued, sid)
		r.snapQueueMu.Unlock()
	}
}

// snapshotWorker publishes queued sessions one at a time until ctx ends.
func (r *Runtime) snapshotWorker(ctx context.Context, ch <-chan string) {
	for {
		select {
		case <-ctx.Done():
			return
		case sid := <-ch:
			r.snapQueueMu.Lock()
			force := r.snapQueued[sid]
			delete(r.snapQueued, sid)
			r.snapQueueMu.Unlock()
			r.maybePushSnapshot(ctx, sid, force)
		}
	}
}

// logPublishFailure logs a failed step: a permanent refusal says it will
// not be retried for this content.
func logPublishFailure(step, sid string, err error, permanent bool, t *publishTrack) {
	if permanent {
		log.Printf("daemon: %s session=%s: %v (not retried until the transcript changes)", step, sid, err)
		return
	}
	log.Printf("daemon: %s session=%s: %v (attempt %d, next in %s)", step, sid, err, t.failures, snapshotBackoff(t.failures))
}

func turnsStamp(turns int) string { return "turns:" + strconv.Itoa(turns) }

// isStrictUUID matches the server's session id check (8-4-4-4-12 hex).
func isStrictUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
