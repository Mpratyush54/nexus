package desktopbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPError is a non-2xx answer from the server's desktop channel.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("desktop channel HTTP %d", e.Status) }

// IsMissingRoute reports whether the server answered 404/405 for a desktop
// channel route, i.e. the connected server predates browser-to-desktop execution.
func IsMissingRoute(e error) bool {
	var h *HTTPError
	return errors.As(e, &h) && (h.Status == 404 || h.Status == 405)
}

type Client struct {
	BaseURL, Token, Key string
	HTTP                *http.Client
}

func (c Client) Call(ctx context.Context, method, path string, body, out any) error {
	u, e := url.Parse(c.BaseURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("desktop execution requires an HTTPS server")
	}
	b, e := json.Marshal(body)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Nexus-Desktop-Key", c.Key)
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	// Never forward a device credential through a server redirect, including
	// when a custom HTTP transport/client is injected by the host.
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := boundedClient.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Status: resp.StatusCode}
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if e != nil {
		return e
	}
	if len(data) > 2<<20 {
		return fmt.Errorf("desktop channel response too large")
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
