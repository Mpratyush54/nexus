package store

import (
	"time"
)

// User represents a team member or account.
type User struct {
	ID        string         `json:"id"`
	Username  string         `json:"username"`
	Email     string         `json:"email,omitempty"`
	Settings  map[string]any `json:"settings,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Project represents a canonical repository or project.
type Project struct {
	ID           string    `json:"id"`
	CanonicalURL string    `json:"canonical_url,omitempty"`
	RootCommit   string    `json:"root_commit,omitempty"`
	FolderName   string    `json:"folder_name"`
	DisplayName  string    `json:"display_name,omitempty"`
	CreatedBy    string    `json:"created_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Workspace represents an active machine checkout of a project.
type Workspace struct {
	ID                    string    `json:"id"`
	ProjectID             string    `json:"project_id"`
	UserID                string    `json:"user_id"`
	MachineID             string    `json:"machine_id"`
	Path                  string    `json:"path"`
	Branch                string    `json:"branch,omitempty"`
	CommitSHA             string    `json:"commit_sha,omitempty"`
	IsDirty               bool      `json:"is_dirty"`
	IsOnline              bool      `json:"is_online"`
	IsDesignatedProcessor bool      `json:"is_designated_processor"`
	LastSeen              time.Time `json:"last_seen"`
	DaemonURL             string    `json:"daemon_url,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

// MemoryItem represents a scoped, confidence-scored unit of collective memory.
type MemoryItem struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"project_id,omitempty"`
	UserID         string    `json:"user_id,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	OrgID          string    `json:"org_id,omitempty"`
	Key            string    `json:"key"`
	Content        string    `json:"content"`
	ContextSnippet string    `json:"context_snippet,omitempty"`
	Level          string    `json:"level"` // organization | project | personal | session
	Scope          string    `json:"scope"` // fact | preference | decision | constraint | pattern | episode_summary
	Embedding      []float32 `json:"embedding,omitempty"`
	Tags           []string  `json:"tags,omitempty"`
	Confidence     float32   `json:"confidence"`
	Status         string    `json:"status"` // PROPOSED | CONFIRMED | REJECTED | SUPERSEDED
	Source         string    `json:"source,omitempty"`
	SourceEventID  int64     `json:"source_event_id,omitempty"`
	ProposedBy     string    `json:"proposed_by,omitempty"`
	ConfirmedBy    string    `json:"confirmed_by,omitempty"`
	SupersededBy   string    `json:"superseded_by,omitempty"`
	UseCount       int       `json:"use_count"`
	LastUsedAt     time.Time `json:"last_used_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Episode captures the narrative arc of a bug investigation, feature, or refactor.
type Episode struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	SessionID     string    `json:"session_id,omitempty"`
	Title         string    `json:"title"`
	EpisodeType   string    `json:"episode_type"` // bug_fix | feature | refactor | incident | investigation
	Trigger       string    `json:"trigger,omitempty"`
	Investigation string    `json:"investigation,omitempty"`
	RootCause     string    `json:"root_cause,omitempty"`
	Resolution    string    `json:"resolution,omitempty"`
	Verification  string    `json:"verification,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	Embedding     []float32 `json:"embedding,omitempty"`
	FilesInvolved []string  `json:"files_involved,omitempty"`
	ErrorPatterns []string  `json:"error_patterns,omitempty"`
	Status        string    `json:"status"` // OPEN | INVESTIGATING | RESOLVED | WONT_FIX
	OpenedAt      time.Time `json:"opened_at"`
	ResolvedAt    time.Time `json:"resolved_at,omitempty"`
	CreatedBy     string    `json:"created_by,omitempty"`
	ResolvedBy    string    `json:"resolved_by,omitempty"`
}

// EpisodeEvent joins an event into an episode's timeline.
type EpisodeEvent struct {
	EpisodeID string    `json:"episode_id"`
	EventID   int64     `json:"event_id"`
	Role      string    `json:"role"` // trigger | investigation | attempt | fix | verification | context
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// WatchedFile represents a tracked configuration or instruction file.
type WatchedFile struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Path        string    `json:"path"`
	LastHash    string    `json:"last_hash"`
	FileType    string    `json:"file_type"` // claude_md | cursorrules | copilot_instructions | windsurfrules | custom
	CreatedAt   time.Time `json:"created_at"`
}

// Task represents an active work item.
type Task struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	EpisodeID   string    `json:"episode_id,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"` // OPEN | IN_PROGRESS | DONE | BLOCKED
	AssignedTo  string    `json:"assigned_to,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Event represents an immutable append-only event row.
type Event struct {
	ID          int64          `json:"id"`
	ProjectID   string         `json:"project_id"`
	SessionID   string         `json:"session_id,omitempty"`
	UserID      string         `json:"user_id,omitempty"`
	AgentID     string         `json:"agent_id,omitempty"`
	WorkspaceID string         `json:"workspace_id,omitempty"`
	EpisodeID   string         `json:"episode_id,omitempty"`
	EventType   string         `json:"event_type"`
	Payload     map[string]any `json:"payload"`
	CreatedAt   time.Time      `json:"created_at"`
}
