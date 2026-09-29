// Package localclient talks to the workspace daemon’s local HTTP surface
// (127.0.0.1:7272) without opening a browser. Shared by the native desktop
// shell and tests so UI and updater stay decoupled from HTML status pages.
package localclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultBase = "http://127.0.0.1:7272"

// Client is a thin HTTP client for daemon local endpoints.
type Client struct {
	Base   string
	HTTP   *http.Client
}

func New(base string) *Client {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = DefaultBase
	}
	return &Client{
		Base: base,
		HTTP: &http.Client{Timeout: 5 * time.Second},
	}
}

// Status is the /local/status JSON shape (subset used by the desktop shell).
type Status struct {
	OK          bool   `json:"ok"`
	Connected   bool   `json:"connected"`
	ServerURL   string `json:"server_url"`
	AppURL      string `json:"app_url"`
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	HasToken    bool   `json:"has_token"`
	WorkspaceID string `json:"workspace_id"`
	Root        string `json:"root"`
	MachineID   string `json:"machine_id"`
	ProxyURL    string `json:"proxy_url"`
	Message     string `json:"message"`
}

// Harvest is a subset of /local/harvest.
type Harvest struct {
	Root            string         `json:"root"`
	LastScanAt      string         `json:"last_scan_at"`
	LastScanFiles   int            `json:"last_scan_files"`
	LastScanTurns   int            `json:"last_scan_turns"`
	TurnsEmitted    int            `json:"turns_emitted"`
	ActiveSessions  int            `json:"active_sessions"`
	ProposalsSaved  int            `json:"proposals_saved"`
	ProposalErrors  int            `json:"proposal_errors"`
	Agents          []HarvestAgent `json:"agents"`
	Recent          []HarvestEvent `json:"recent"`
}

type HarvestAgent struct {
	Name      string `json:"name"`
	Agent     string `json:"agent"`
	Format    string `json:"format"`
	Kind      string `json:"kind"`
	FilesSeen int    `json:"files_seen"`
	FileCount int    `json:"file_count"`
}

type HarvestEvent struct {
	At      string `json:"at"`
	Time    string `json:"time"`
	Type    string `json:"type"`
	Event   string `json:"event"`
	Agent   string `json:"agent"`
	Detail  string `json:"detail"`
	Message string `json:"message"`
}

func (c *Client) Online() bool {
	st, err := c.GetStatus()
	return err == nil && st != nil
}

func (c *Client) GetStatus() (*Status, error) {
	var st Status
	if err := c.getJSON("/local/status", &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *Client) GetHarvest() (*Harvest, error) {
	var h Harvest
	if err := c.getJSON("/local/harvest", &h); err != nil {
		return nil, err
	}
	return &h, nil
}

func (c *Client) TriggerHarvest() error {
	resp, err := c.HTTP.Post(c.Base+"/local/harvest", "application/json", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("harvest: %s", resp.Status)
	}
	return nil
}

func (c *Client) BrowserLogin() error {
	resp, err := c.HTTP.Post(c.Base+"/local/browser-login", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("browser-login: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) Logout() error {
	resp, err := c.HTTP.Post(c.Base+"/local/logout", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("logout: %s", resp.Status)
	}
	return nil
}

func (c *Client) SwitchWorkspace(path string) error {
	body, _ := json.Marshal(map[string]string{"path": path})
	resp, err := c.HTTP.Post(c.Base+"/local/workspace/switch", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("workspace switch: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) RecentWorkspaces() ([]string, error) {
	var raw json.RawMessage
	if err := c.getJSON("/local/workspace/recent", &raw); err != nil {
		return nil, err
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	var wrap struct {
		Items []string `json:"items"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, err
	}
	return wrap.Items, nil
}

func (c *Client) getJSON(path string, dest any) error {
	resp, err := c.HTTP.Get(c.Base + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dest)
}
