package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// Config carries the CLI's connection settings. Values come from global
// flags with environment-variable fallbacks (see defaultConfig).
type Config struct {
	ServerURL   string
	Token       string
	DaemonURL   string
	DaemonToken string
	ProjectID   string
	JSON        bool
}

// defaultConfig resolves connection settings from the environment.
// Ports are placeholders until the daemon/server entrypoints lock them;
// every value is overridable via global flags.
func defaultConfig() Config {
	return Config{
		ServerURL:   firstNonEmpty(os.Getenv("NEXUS_SERVER"), os.Getenv("CENTRAL_MEMORY_SERVER"), "http://localhost:8080"),
		Token:       firstNonEmpty(os.Getenv("NEXUS_TOKEN"), os.Getenv("CENTRAL_MEMORY_TOKEN"), ""),
		DaemonURL:   firstNonEmpty(os.Getenv("NEXUS_DAEMON"), "http://localhost:7171"),
		DaemonToken: os.Getenv("NEXUS_DAEMON_TOKEN"),
		ProjectID:   firstNonEmpty(os.Getenv("NEXUS_PROJECT"), ""),
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// APIClient is a thin stdlib HTTP client for the central server.
// Auth is a Bearer token when configured (Phase 1.8 JWT stub).
type APIClient struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func newAPIClient(cfg Config) *APIClient {
	return &APIClient{
		Base:  strings.TrimSuffix(strings.TrimSpace(cfg.ServerURL), "/"),
		Token: cfg.Token,
		HTTP:  &http.Client{Timeout: 15 * time.Second},
	}
}

// httpError carries a non-2xx server response.
type httpError struct {
	Status  int
	Message string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("server returned %d: %s", e.Status, e.Message)
}

func isNotFound(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

func isMethodNotAllowed(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.Status == http.StatusMethodNotAllowed
}

// errNotImplemented marks a command whose server route does not exist yet.
// The Phase tag points at the implementation-plan section that will add it.
type errNotImplemented struct {
	Feature string
	Phase   string
}

func (e *errNotImplemented) Error() string {
	return fmt.Sprintf("%s is not implemented by the central server yet (%s); see implementation-plan.md", e.Feature, e.Phase)
}

func notImplementedFor(feature string, err error) error {
	if isNotFound(err) || isMethodNotAllowed(err) {
		return &errNotImplemented{Feature: feature, Phase: phaseFor(feature)}
	}
	return err
}

func phaseFor(feature string) string {
	switch {
	case strings.HasPrefix(feature, "memory confirm"),
		strings.HasPrefix(feature, "memory reject"):
		return "Phase 2 confirmation flow"
	case strings.HasPrefix(feature, "session"):
		return "Phase 3 sessions"
	case strings.HasPrefix(feature, "branch"):
		return "Phase 5 memory branching"
	default:
		return "a later phase"
	}
}

// do performs one JSON request and returns the raw response body.
func (c *APIClient) do(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	u := c.Base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", u, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &httpError{Status: resp.StatusCode, Message: parseErrorMessage(resp.StatusCode, data)}
	}
	return data, nil
}

// getJSON performs a GET and decodes the JSON response into dst.
func (c *APIClient) getJSON(ctx context.Context, path string, query url.Values, dst any) error {
	data, err := c.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	return nil
}

// postJSON performs a POST with an optional JSON body and decodes the
// JSON response into dst (nil dst skips decoding).
func (c *APIClient) postJSON(ctx context.Context, path string, body, dst any) error {
	data, err := c.do(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return err
	}
	if dst == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	return nil
}

// parseErrorMessage extracts the message from the server's standard
// {"error":{"code":...,"message":...}} envelope, falling back to raw text.
func parseErrorMessage(status int, data []byte) string {
	var env struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &env) == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	if s := strings.TrimSpace(string(data)); s != "" {
		return s
	}
	if text := http.StatusText(status); text != "" {
		return text
	}
	return "unknown error"
}

// renderTable formats headers+rows as tabwriter-aligned text. It is a pure
// function so table layout is unit-testable without network or stdout.
func renderTable(headers []string, rows [][]string) string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
	return buf.String()
}

func printTable(w io.Writer, headers []string, rows [][]string) {
	fmt.Fprint(w, renderTable(headers, rows))
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// truncate collapses whitespace and caps s at n characters for table cells.
func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// strField extracts the first present string field from a generic JSON map,
// so stub commands stay resilient to future server response shapes.
func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				return t
			case bool:
				if t {
					return "true"
				}
				return "false"
			case float64:
				return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.2f", t), "0"), ".")
			}
		}
	}
	return ""
}
