// Package mcpproxy is the stdio→HTTPS MCP bridge (spec 7.5, D7).
// It forwards each JSON-RPC frame unchanged to POST {base}/v1/agent/mcp.
// No local DB, no cache, no TCP listener — outbound HTTPS only.
package mcpproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	mcpPath      = "/v1/agent/mcp"
	maxScanBytes = 10 * 1024 * 1024
	maxBody      = 8 << 20
)

// Config is the cloud MCP endpoint the proxy posts to.
type Config struct {
	// BaseURL is the API root (e.g. https://api-nexus.pratyushes.dev).
	BaseURL string
	// Token is the Bearer credential (NEXUS_TOKEN / CENTRAL_MEMORY_TOKEN).
	Token string
	// ProjectID is sent as X-Nexus-Project when non-empty (NEXUS_PROJECT).
	ProjectID string
	// HTTP is optional; defaults to a 30s client.
	HTTP *http.Client
}

// FromEnv builds Config from the standard Nexus env vars and optional base.
func FromEnv(baseFallback string) Config {
	base := firstNonEmpty(
		os.Getenv("NEXUS_SERVER"),
		os.Getenv("CENTRAL_SERVER_URL"),
		baseFallback,
	)
	return Config{
		BaseURL:   base,
		Token:     firstNonEmpty(os.Getenv("NEXUS_TOKEN"), os.Getenv("CENTRAL_MEMORY_TOKEN"), os.Getenv("CENTRAL_SERVER_TOKEN")),
		ProjectID: strings.TrimSpace(os.Getenv("NEXUS_PROJECT")),
	}
}

func (c Config) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c Config) endpoint() string {
	return strings.TrimSuffix(strings.TrimSpace(c.BaseURL), "/") + mcpPath
}

// Forward POSTs one JSON-RPC frame to /v1/agent/mcp and returns the response
// body. A nil body with nil error means HTTP 204 (notification: no reply).
func (c Config) Forward(ctx context.Context, frame []byte) ([]byte, error) {
	base := strings.TrimSpace(c.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("mcp-proxy: server URL is required (NEXUS_SERVER)")
	}
	token := strings.TrimSpace(c.Token)
	if token == "" {
		return nil, fmt.Errorf("mcp-proxy: bearer token is required (NEXUS_TOKEN)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if pid := strings.TrimSpace(c.ProjectID); pid != "" {
		req.Header.Set("X-Nexus-Project", pid)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("mcp-proxy: server %s: %s", resp.Status, msg)
	}
	return body, nil
}

// Serve reads newline-delimited JSON-RPC from r and writes responses to w.
// It returns on EOF or ctx cancel. Empty lines are skipped. Notifications
// that yield no response body are not written.
func (c Config) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxScanBytes)
	out := bufio.NewWriter(w)
	defer out.Flush()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return fmt.Errorf("mcp-proxy stdio read: %w", err)
			}
			return nil
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		body, err := c.Forward(ctx, []byte(line))
		if err != nil {
			// Surface transport failures as JSON-RPC errors so hosts keep working.
			fail, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      nil,
				"error":   map[string]any{"code": -32603, "message": err.Error()},
			})
			if _, werr := out.Write(append(fail, '\n')); werr != nil {
				return werr
			}
			out.Flush()
			continue
		}
		if len(body) == 0 {
			continue
		}
		if _, err := out.Write(append(bytes.TrimSpace(body), '\n')); err != nil {
			return fmt.Errorf("mcp-proxy stdio write: %w", err)
		}
		out.Flush()
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
