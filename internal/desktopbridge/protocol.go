// Package desktopbridge implements the authenticated, outbound-only desktop
// execution channel. It never exposes a listening localhost execution port.
package desktopbridge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrConflict = errors.New("desktop channel conflict")
var ErrNotFound = errors.New("desktop channel not found")

const DeviceLifetime = 45 * time.Second
const ApprovalLifetime = 3 * time.Minute
const RunLifetime = 20 * time.Minute

type Device struct {
	ID        string    `json:"id"`
	Owner     string    `json:"-"`
	KeyHash   string    `json:"-"`
	Name      string    `json:"name"`
	Projects  []string  `json:"projects"`
	Harnesses []string  `json:"harnesses"`
	Seen      time.Time `json:"last_seen_at"`
}

type Request struct {
	ID        string `json:"id"`
	DeviceID  string `json:"device_id"`
	SessionID string `json:"session_id"`
	ProjectID string `json:"project_id"`
	Kind      string `json:"kind"` // models or turn; no arbitrary RPC or argv
	Agent     string `json:"agent"`
	Model     string `json:"model,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
}

type Job struct {
	Request
	Owner      string            `json:"-"`
	State      string            `json:"state"`
	Revision   int64             `json:"revision"`
	Created    time.Time         `json:"created_at"`
	Updated    time.Time         `json:"updated_at"`
	Expires    time.Time         `json:"expires_at"`
	Status     string            `json:"status,omitempty"`
	Text       string            `json:"text,omitempty"`
	Activities []json.RawMessage `json:"activities,omitempty"`
	Result     json.RawMessage   `json:"result,omitempty"`
	Error      string            `json:"error,omitempty"`
}

type Update struct {
	Revision   int64             `json:"revision"`
	State      string            `json:"state"`
	Status     string            `json:"status,omitempty"`
	Text       string            `json:"text,omitempty"`
	Activities []json.RawMessage `json:"activities,omitempty"`
	Result     json.RawMessage   `json:"result,omitempty"`
	Error      string            `json:"error,omitempty"`
}

func Terminal(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled" || state == "expired"
}
func HashKey(key string) string { sum := sha256.Sum256([]byte(key)); return hex.EncodeToString(sum[:]) }
func (d Device) Authenticate(key string) bool {
	return len(key) >= 32 && subtle.ConstantTimeCompare([]byte(d.KeyHash), []byte(HashKey(key))) == 1
}
func Contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func ValidAgent(agent string) bool {
	return Contains([]string{"codex", "cursor", "opencode", "claude"}, agent)
}

func (r Request) Validate() error {
	if r.ID == "" || len(r.ID) > 80 || strings.ContainsAny(r.ID, "/\\\x00") || r.DeviceID == "" || r.ProjectID == "" || !ValidAgent(r.Agent) {
		return errors.New("valid request, device, project and harness are required")
	}
	if r.Kind != "turn" && r.Kind != "models" {
		return errors.New("unsupported desktop request kind")
	}
	if len(r.Model) > 160 || strings.HasPrefix(r.Model, "-") || strings.ContainsAny(r.Model, "\r\n\x00") {
		return errors.New("invalid model ID")
	}
	if r.Kind == "turn" && (r.SessionID == "" || strings.TrimSpace(r.Prompt) == "" || len(r.Prompt) > 16000) {
		return errors.New("a session and message of up to 16,000 bytes are required")
	}
	return nil
}

// Apply validates every state transition. Worker updates cannot resurrect a
// cancelled job, replay old progress, or extend the execution deadline.
func Apply(job Job, update Update, now time.Time) (Job, error) {
	if Terminal(job.State) || update.Revision != job.Revision || now.After(job.Expires) {
		return job, ErrConflict
	}
	allowed := update.State == "failed" || (job.State == "awaiting_approval" && update.State == "running") || (job.State == "running" && (update.State == "running" || update.State == "completed"))
	if !allowed {
		return job, ErrConflict
	}
	if len(update.Text) > 512<<10 || len(update.Status) > 1000 || len(update.Error) > 4000 || len(update.Result) > 1<<20 || len(update.Activities) > 100 {
		return job, errors.New("desktop output is too large")
	}
	for _, activity := range update.Activities {
		if len(activity) > 64<<10 || !json.Valid(activity) {
			return job, errors.New("invalid tool activity")
		}
	}
	if len(update.Result) > 0 && !json.Valid(update.Result) {
		return job, errors.New("invalid desktop result")
	}
	if job.State == "awaiting_approval" && update.State == "running" {
		job.Expires = now.Add(RunLifetime)
	}
	job.State, job.Status, job.Text, job.Activities, job.Result, job.Error = update.State, update.Status, update.Text, update.Activities, update.Result, update.Error
	job.Revision++
	job.Updated = now
	return job, nil
}

// Repository must atomically claim/update jobs and enforce owner/device scope.
// A claimed request is NEVER re-queued after worker failure: repeating edits is
// worse than reporting an interrupted request.
type Repository interface {
	Register(context.Context, Device) error
	Devices(context.Context, string) ([]Device, error)
	Device(context.Context, string, string) (Device, error)
	Create(context.Context, Job) (Job, error)
	Claim(context.Context, string, string, time.Time) (*Job, error)
	Get(context.Context, string, string) (Job, error)
	Update(context.Context, string, string, string, Update, time.Time) (Job, error)
	Cancel(context.Context, string, string, time.Time) (Job, error)
}
