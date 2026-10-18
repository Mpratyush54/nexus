package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("cloud: %s %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}
