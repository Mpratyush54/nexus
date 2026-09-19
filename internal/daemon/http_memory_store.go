package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// HTTPMemoryStore uploads processor proposals to the central server
// (POST /memory) so harvested agent conversations appear in the portal
// without requiring MCP tool calls. Implements MemoryStore.
type HTTPMemoryStore struct {
	Base      string
	Token     string
	ProjectID string
	HTTP      *http.Client

	mu    sync.Mutex
	local []MemoryRecord // recent saves for in-process dedup
}

// NewHTTPMemoryStore builds a portal-backed store. projectID must be the
// server UUID from /projects/resolve (not the folder basename).
func NewHTTPMemoryStore(base, token, projectID string) *HTTPMemoryStore {
	return &HTTPMemoryStore{
		Base:      strings.TrimSuffix(strings.TrimSpace(base), "/"),
		Token:     strings.TrimSpace(token),
		ProjectID: strings.TrimSpace(projectID),
		HTTP:      &http.Client{Timeout: 20 * time.Second},
	}
}

// SetProjectID updates the target project (call after Register resolves UUID).
func (s *HTTPMemoryStore) SetProjectID(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.ProjectID = strings.TrimSpace(id)
	s.mu.Unlock()
}

// Existing implements MemoryStore (local cache + optional search).
func (s *HTTPMemoryStore) Existing() []MemoryRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MemoryRecord(nil), s.local...)
}

// portalLevel coerces processor levels into values POST /memory accepts
// without extra session/user linkage. Heuristic ClassifyLevel defaults to
// "session", but the server rejects session rows without session_id (500).
// Harvested agent chats have no portal session — promote to project.
func portalLevel(level MemoryLevel) string {
	switch level {
	case "", LevelSession, LevelEphemeral:
		return string(LevelProject)
	default:
		return string(level)
	}
}

// Save implements MemoryStore by POSTing a PROPOSED memory to the server.
func (s *HTTPMemoryStore) Save(p Proposal) error {
	if s == nil {
		return fmt.Errorf("daemon: http memory store is nil")
	}
	s.mu.Lock()
	projectID := s.ProjectID
	base := s.Base
	token := s.Token
	s.mu.Unlock()
	if base == "" || projectID == "" {
		return fmt.Errorf("daemon: http memory store missing server or project_id")
	}
	level := portalLevel(p.Level)
	scope := string(p.Scope)
	if scope == "" {
		scope = "fact"
	}
	source := strings.TrimSpace(p.Source)
	if source == "" {
		source = "daemon:harvester"
	}
	body := map[string]any{
		"project_id": projectID,
		"key":        p.Key,
		"content":    p.Content,
		"level":      level,
		"scope":      scope,
		"source":     source,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		base+"/memory", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("daemon: memory post: %w", err)
	}
	defer resp.Body.Close()
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(errBody))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("daemon: memory post: %s", msg)
	}
	s.mu.Lock()
	s.local = append(s.local, MemoryRecord{
		Key:        p.Key,
		Content:    p.Content,
		Level:      MemoryLevel(level),
		Scope:      p.Scope,
		Confidence: p.Confidence,
	})
	// Cap local dedup cache.
	if len(s.local) > 500 {
		s.local = s.local[len(s.local)-400:]
	}
	s.mu.Unlock()
	return nil
}

// PrefetchExisting loads recent keys from the server for better dedup.
func (s *HTTPMemoryStore) PrefetchExisting(ctx context.Context) error {
	if s == nil || s.Base == "" || s.ProjectID == "" {
		return nil
	}
	q := url.Values{}
	q.Set("project_id", s.ProjectID)
	q.Set("q", "")
	q.Set("limit", "50")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.Base+"/memory/search?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil // non-fatal
	}
	var out struct {
		Items []struct {
			Key        string  `json:"key"`
			Content    string  `json:"content"`
			Level      string  `json:"level"`
			Scope      string  `json:"scope"`
			Confidence float64 `json:"confidence"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&out); err != nil {
		return nil
	}
	s.mu.Lock()
	for _, it := range out.Items {
		s.local = append(s.local, MemoryRecord{
			Key:        it.Key,
			Content:    it.Content,
			Level:      MemoryLevel(it.Level),
			Scope:      MemoryScope(it.Scope),
			Confidence: it.Confidence,
		})
	}
	s.mu.Unlock()
	return nil
}

// ExtractRemote posts conversation turns to POST /memory/extract so the
// central server (OpenRouter key) creates PROPOSED memories. Returns the
// provider name and proposals mirrored into the local dedup cache.
func (s *HTTPMemoryStore) ExtractRemote(ctx context.Context, turns []map[string]string) (provider string, proposals []Proposal, err error) {
	if s == nil {
		return "", nil, fmt.Errorf("daemon: http memory store is nil")
	}
	s.mu.Lock()
	projectID := s.ProjectID
	base := s.Base
	token := s.Token
	existing := append([]MemoryRecord(nil), s.local...)
	s.mu.Unlock()
	if base == "" || projectID == "" {
		return "", nil, fmt.Errorf("daemon: http memory store missing server or project_id")
	}
	if len(turns) == 0 {
		return "heuristic", nil, nil
	}
	existPayload := make([]map[string]string, 0, len(existing))
	for _, e := range existing {
		if strings.TrimSpace(e.Content) == "" {
			continue
		}
		existPayload = append(existPayload, map[string]string{
			"level":   string(e.Level),
			"scope":   string(e.Scope),
			"content": e.Content,
		})
		if len(existPayload) >= 30 {
			break
		}
	}
	body := map[string]any{
		"project_id": projectID,
		"turns":      turns,
		"source":     "daemon:harvester",
		"existing":   existPayload,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/memory/extract", bytes.NewReader(raw))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("daemon: memory extract: %w", err)
	}
	defer resp.Body.Close()
	rawResp, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(rawResp))
		if msg == "" {
			msg = resp.Status
		}
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return "", nil, fmt.Errorf("daemon: memory extract: %s", msg)
	}
	var out struct {
		Provider string `json:"provider"`
		Items    []struct {
			Key        string  `json:"key"`
			Content    string  `json:"content"`
			Level      string  `json:"level"`
			Scope      string  `json:"scope"`
			Confidence float64 `json:"confidence"`
			Source     string  `json:"source"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rawResp, &out); err != nil {
		return "", nil, fmt.Errorf("daemon: memory extract decode: %w", err)
	}
	now := time.Now().UTC()
	for _, it := range out.Items {
		p := Proposal{
			Key:        it.Key,
			Content:    it.Content,
			Level:      MemoryLevel(it.Level),
			Scope:      MemoryScope(it.Scope),
			Confidence: it.Confidence,
			Source:     it.Source,
			ProposedAt: now,
		}
		proposals = append(proposals, p)
		s.mu.Lock()
		s.local = append(s.local, MemoryRecord{
			Key:        it.Key,
			Content:    it.Content,
			Level:      MemoryLevel(it.Level),
			Scope:      MemoryScope(it.Scope),
			Confidence: it.Confidence,
		})
		if len(s.local) > 500 {
			s.local = s.local[len(s.local)-400:]
		}
		s.mu.Unlock()
	}
	provider = out.Provider
	if provider == "" {
		provider = "heuristic"
	}
	return provider, proposals, nil
}
