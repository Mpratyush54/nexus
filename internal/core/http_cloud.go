package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"central-memory/internal/diagnostics"
)

// HTTPCloud is the native engine's direct authenticated cloud transport. It
// deliberately has no localhost fallback: the desktop talks to the portal
// over HTTPS, while capture runs in-process beside it.
type HTTPCloud struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func (c *HTTPCloud) Do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" || strings.TrimSpace(c.Token) == "" {
		return nil, ErrOffline
	}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Token))
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.Client
	if client == nil {
		client = cloudClient(cloudTimeout(method, path))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	limit := int64(2 << 20)
	if method == http.MethodGet && strings.Contains(path, "/blobs/") {
		limit = 32 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("cloud: response too large for %s", path)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		cerr := newCloudError(resp.StatusCode, resp.Status, raw)
		cerr.RequestID = diagnostics.ResponseRequestID(resp.Header, raw)
		logPath := path
		if i := strings.IndexAny(logPath, "?#"); i >= 0 {
			logPath = logPath[:i] // queries can carry search text
		}
		log.Printf("WARN cloud: %s %s -> %d ref=%s: %s", method, logPath, resp.StatusCode, orDash(cerr.RequestID), cerr.Msg)
		return nil, cerr
	}
	return raw, nil
}

// Default per-request timeouts for HTTPCloud when no Client is supplied.
const (
	cloudDefaultTimeout      = 30 * time.Second
	cloudBlobTimeout         = 2 * time.Minute
	cloudBriefRefreshTimeout = 120 * time.Second
)

// cloudTimeout picks the client timeout for one request. Blob transfers and
// POST /v1/projects/{id}/brief/refresh (which the server may spend up to 90s
// generating) get longer limits; everything else keeps the 30s default.
func cloudTimeout(method, path string) time.Duration {
	p := path
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	switch {
	case strings.Contains(path, "/blobs/"):
		return cloudBlobTimeout
	case method == http.MethodPost && strings.HasPrefix(p, "/v1/projects/") && strings.HasSuffix(p, "/brief/refresh"):
		return cloudBriefRefreshTimeout
	}
	return cloudDefaultTimeout
}

// cloudTransport is shared by every HTTPCloud request. A fresh client per
// call fell back to http.DefaultTransport, which keeps only two idle
// connections per host: opening a chat fires about ten requests at once, so
// most of them paid a new TCP+TLS handshake every time. One transport with a
// larger idle pool keeps those connections warm (and HTTP/2 when the server
// offers it). Compression stays enabled, so responses arrive gzip-encoded and
// are decoded transparently.
var cloudTransport = newCloudTransport()

func newCloudTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 64
	t.MaxIdleConnsPerHost = 16
	t.IdleConnTimeout = 90 * time.Second
	t.ForceAttemptHTTP2 = true
	t.DisableCompression = false
	t.DisableKeepAlives = false
	return t
}

// cloudClients caches one client per timeout; all share cloudTransport.
var cloudClients sync.Map // time.Duration -> *http.Client

func cloudClient(timeout time.Duration) *http.Client {
	if client, ok := cloudClients.Load(timeout); ok {
		return client.(*http.Client)
	}
	client, _ := cloudClients.LoadOrStore(timeout, &http.Client{Transport: cloudTransport, Timeout: timeout})
	return client.(*http.Client)
}

// CloudError is a non-2xx answer from the cloud API. Its message is the
// server's human sentence ("plan limit reached: ...") rather than the raw
// JSON body, because apps show it to people as-is.
//
// RequestID is the server's X-Request-ID / error.request_id. It is shown as
// "(ref ABC123)" so a person can quote it and support can find the
// matching server log line.
type CloudError struct {
	Status    int
	Msg       string
	RequestID string
}

func (e *CloudError) Error() string {
	if e.RequestID != "" {
		return e.Msg + " (ref " + e.RequestID + ")"
	}
	return e.Msg
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newCloudError(code int, status string, raw []byte) *CloudError {
	body := strings.TrimSpace(string(raw))
	var parsed struct {
		Error json.RawMessage `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &parsed) == nil && len(parsed.Error) > 0 {
		var inner struct {
			Message string `json:"message"`
		}
		var plain string
		if json.Unmarshal(parsed.Error, &inner) == nil && inner.Message != "" {
			msg = inner.Message
		} else if json.Unmarshal(parsed.Error, &plain) == nil {
			msg = plain
		}
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = status
		if body != "" && !strings.HasPrefix(body, "<") && len(body) <= 300 {
			msg += ": " + body
		}
	}
	return &CloudError{Status: code, Msg: capitalizeFirst(msg)}
}

// capitalizeFirst upper-cases the first rune (not byte) of s.
func capitalizeFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
