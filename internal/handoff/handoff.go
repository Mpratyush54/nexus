// Package handoff implements the session & task handoff protocol
// (nexus issue #23): one-command transfer of active work between humans
// (Alice -> Bob) and between agents (Claude -> Copilot/Cursor/OpenCode).
//
// The package is intentionally stdlib-only and store-agnostic: it defines
// its own lightweight snapshots (TaskStatus, MemorySnippet, BranchState)
// instead of importing internal/store, so daemons, the MCP server, and the
// WebSocket hub can all build/translate handoffs without dragging in a DB
// dependency. Persistence of the emitted events (SESSION_HANDOFF_INITIATED /
// SESSION_HANDOFF_ACCEPTED) is the caller's job via store.AppendEvent.
package handoff

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Wire-visible event types. Callers persist these via the append-only event
// log; the payload carries the handoff ID plus from/to routing.
const (
	EventHandoffInitiated = "SESSION_HANDOFF_INITIATED"
	EventHandoffAccepted  = "SESSION_HANDOFF_ACCEPTED"
)

// Agent families understood by TranslateForAgent. Anything else falls back
// to a generic markdown rendering.
const (
	AgentClaude  = "claude"
	AgentCopilot = "copilot"
	AgentCursor  = "cursor"
)

// ErrLocked is returned when a file is already advisory-locked by someone
// else. ErrConflict covers double-accept and wrong-recipient acceptance.
var (
	ErrLocked   = errors.New("handoff: file already locked")
	ErrConflict = errors.New("handoff: conflicting accept")
)

// TaskStatus is a snapshot of the active task at handoff time. Status
// mirrors store.Task (OPEN | IN_PROGRESS | DONE | BLOCKED).
type TaskStatus struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	AssignedTo  string `json:"assigned_to,omitempty"`
}

// MemorySnippet is one ephemeral working memory carried with the handoff
// (session-level items the receiver needs, not the whole memory store).
type MemorySnippet struct {
	Key     string `json:"key"`
	Content string `json:"content"`
	Level   string `json:"level,omitempty"`
}

// BranchState captures the working branch so the receiver can check out /
// verify the same tree before touching locked files.
type BranchState struct {
	Branch    string `json:"branch,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	IsDirty   bool   `json:"is_dirty"`
}

// HandoffPackage is the transferable unit: active task + ephemeral
// memories + modified-file pointers + branch state, routed from one
// user/agent to another.
type HandoffPackage struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id"`
	SessionID     string          `json:"session_id,omitempty"`
	FromUser      string          `json:"from_user"`
	ToUser        string          `json:"to_user"`
	FromAgent     string          `json:"from_agent,omitempty"`
	ToAgent       string          `json:"to_agent,omitempty"`
	Task          TaskStatus      `json:"task"`
	Memories      []MemorySnippet `json:"memories,omitempty"`
	ModifiedFiles []string        `json:"modified_files,omitempty"`
	Branch        BranchState     `json:"branch"`
	Note          string          `json:"note,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	AcceptedAt    time.Time       `json:"accepted_at,omitempty"`
	Accepted      bool            `json:"accepted"`
	AcceptedBy    string          `json:"accepted_by,omitempty"`
}

// Event is the append-only log envelope for handoff lifecycle transitions.
// Payload is a plain map so store.Event.Payload can carry it unmodified.
type Event struct {
	Type      string         `json:"event_type"`
	HandoffID string         `json:"handoff_id"`
	ProjectID string         `json:"project_id"`
	SessionID string         `json:"session_id,omitempty"`
	FromUser  string         `json:"from_user"`
	ToUser    string         `json:"to_user"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UTC().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

// BuildHandoff collects the active task, ephemeral memories, modified-file
// list, and branch state into a HandoffPackage and returns the
// SESSION_HANDOFF_INITIATED event the caller should persist. Slices are
// deep-copied so later caller mutations cannot corrupt the package.
func BuildHandoff(projectID, sessionID, fromUser, toUser string, task TaskStatus, memories []MemorySnippet, files []string, branch BranchState) (*HandoffPackage, Event) {
	return BuildHandoffFull(projectID, sessionID, fromUser, toUser, "", "", task, memories, files, branch, "")
}

// BuildHandoffFull is BuildHandoff with agent routing and a free-text note.
func BuildHandoffFull(projectID, sessionID, fromUser, toUser, fromAgent, toAgent string, task TaskStatus, memories []MemorySnippet, files []string, branch BranchState, note string) (*HandoffPackage, Event) {
	now := time.Now().UTC()
	pkg := &HandoffPackage{
		ID:        newID("handoff"),
		ProjectID: projectID,
		SessionID: sessionID,
		FromUser:  fromUser,
		ToUser:    toUser,
		FromAgent: fromAgent,
		ToAgent:   toAgent,
		Task:      task,
		Branch:    branch,
		Note:      note,
		CreatedAt: now,
	}
	if len(memories) > 0 {
		pkg.Memories = append([]MemorySnippet(nil), memories...)
	}
	if len(files) > 0 {
		pkg.ModifiedFiles = append([]string(nil), files...)
	}
	ev := Event{
		Type:      EventHandoffInitiated,
		HandoffID: pkg.ID,
		ProjectID: projectID,
		SessionID: sessionID,
		FromUser:  fromUser,
		ToUser:    toUser,
		Payload: map[string]any{
			"handoff_id":     pkg.ID,
			"task_id":        task.ID,
			"task_status":    task.Status,
			"files":          append([]string(nil), pkg.ModifiedFiles...),
			"branch":         branch.Branch,
			"to_agent":       toAgent,
			"memories_count": len(pkg.Memories),
		},
		CreatedAt: now,
	}
	return pkg, ev
}

// AcceptHandoff applies a handoff to the receiving side: it validates the
// recipient, flips the package to accepted, and re-points the carried task
// at the accepter. It returns the SESSION_HANDOFF_ACCEPTED event the caller
// should persist. The package is mutated in place (Accepted/AcceptedBy/
// AcceptedAt/Task.AssignedTo) and also returned for chaining.
func AcceptHandoff(pkg *HandoffPackage, byUser string) (*HandoffPackage, Event, error) {
	if pkg == nil {
		return nil, Event{}, errors.New("handoff: nil package")
	}
	if pkg.Accepted {
		return nil, Event{}, fmt.Errorf("%w: %s already accepted by %s", ErrConflict, pkg.ID, pkg.AcceptedBy)
	}
	if pkg.ToUser != "" && byUser != "" && byUser != pkg.ToUser {
		return nil, Event{}, fmt.Errorf("%w: handoff for %s, accept by %s", ErrConflict, pkg.ToUser, byUser)
	}
	now := time.Now().UTC()
	pkg.Accepted = true
	pkg.AcceptedBy = byUser
	pkg.AcceptedAt = now
	if byUser != "" {
		pkg.Task.AssignedTo = byUser
	}
	ev := Event{
		Type:      EventHandoffAccepted,
		HandoffID: pkg.ID,
		ProjectID: pkg.ProjectID,
		SessionID: pkg.SessionID,
		FromUser:  pkg.FromUser,
		ToUser:    byUser,
		Payload: map[string]any{
			"handoff_id":  pkg.ID,
			"task_id":     pkg.Task.ID,
			"accepted_by": byUser,
		},
		CreatedAt: now,
	}
	return pkg, ev, nil
}

// LockSet is an advisory file-lock map: file path -> owning user. It
// prevents conflicting writes across active workspaces during a handoff
// window (sender locks modified files, receiver waits or steals them
// explicitly). Locks are cooperative — enforcement lives in the daemon /
// file-watcher path, not the OS — so a crashed holder never wedges the
// tree: callers release on accept/cancel, and holders are visible for
// manual override.
type LockSet struct {
	mu      sync.Mutex
	holders map[string]string
}

// NewLockSet returns an empty advisory lock map.
func NewLockSet() *LockSet {
	return &LockSet{holders: make(map[string]string)}
}

// Acquire locks file for owner. It returns an *ErrLocked-wrapped error when
// another owner holds the file; re-acquiring your own lock is a no-op.
func (l *LockSet) Acquire(file, owner string) error {
	file = strings.TrimSpace(file)
	if file == "" {
		return errors.New("handoff: empty file path")
	}
	if owner == "" {
		return errors.New("handoff: empty owner")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if holder, ok := l.holders[file]; ok {
		if holder == owner {
			return nil
		}
		return fmt.Errorf("%w: %s held by %s", ErrLocked, file, holder)
	}
	l.holders[file] = owner
	return nil
}

// Release frees file when held by owner. Releasing a file held by someone
// else (or not held at all) is an error so stray unlocks stay visible.
func (l *LockSet) Release(file, owner string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	holder, ok := l.holders[file]
	if !ok {
		return fmt.Errorf("%w: %s not locked", ErrConflict, file)
	}
	if holder != owner {
		return fmt.Errorf("%w: %s held by %s, release by %s", ErrConflict, file, holder, owner)
	}
	delete(l.holders, file)
	return nil
}

// Holder reports the current owner of file, or "" when unlocked.
func (l *LockSet) Holder(file string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.holders[file]
}

// LockAll acquires every file in files for owner, rolling back partial
// acquisitions on first conflict so a failed handoff leaves no residue.
func (l *LockSet) LockAll(files []string, owner string) error {
	var acquired []string
	for _, f := range files {
		if err := l.Acquire(f, owner); err != nil {
			for _, a := range acquired {
				_ = l.releaseLocked(a)
			}
			return err
		}
		acquired = append(acquired, strings.TrimSpace(f))
	}
	return nil
}

// UnlockAll releases every file in files held by owner; files not held by
// owner are skipped so accept-path cleanup is idempotent.
func (l *LockSet) UnlockAll(files []string, owner string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range files {
		if l.holders[strings.TrimSpace(f)] == owner {
			delete(l.holders, strings.TrimSpace(f))
		}
	}
}

func (l *LockSet) releaseLocked(file string) error {
	holder, ok := l.holders[file]
	if !ok {
		return fmt.Errorf("%w: %s not locked", ErrConflict, file)
	}
	_ = holder
	delete(l.holders, file)
	return nil
}

// Translation is a per-agent rendering of a HandoffPackage. Format names
// the consumer surface ("mcp-context-block" vs "instruction-file" vs
// "markdown"); Content is ready to inject there verbatim.
type Translation struct {
	Agent   string `json:"agent"`
	Format  string `json:"format"`
	Content string `json:"content"`
}

// TranslateForAgent renders the session state into the incoming agent's
// preferred interface: an MCP context block for Claude-family agents, an
// instruction-file update (Copilot/Cursor style) for file-driven agents,
// and plain markdown for anything else. It never returns nil content —
// even an empty package yields a valid (skeletal) block.
func TranslateForAgent(pkg *HandoffPackage, agent string) Translation {
	kind := strings.ToLower(strings.TrimSpace(agent))
	if pkg == nil {
		pkg = &HandoffPackage{}
	}
	switch {
	case strings.Contains(kind, AgentClaude):
		return Translation{Agent: agent, Format: "mcp-context-block", Content: renderMCPBlock(pkg)}
	case strings.Contains(kind, AgentCopilot) || strings.Contains(kind, AgentCursor):
		return Translation{Agent: agent, Format: "instruction-file", Content: renderInstructionFile(pkg, kind)}
	default:
		return Translation{Agent: agent, Format: "markdown", Content: renderMarkdown(pkg)}
	}
}

func xmlEsc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func renderMCPBlock(pkg *HandoffPackage) string {
	var b strings.Builder
	b.WriteString("<handoff_context>\n")
	fmt.Fprintf(&b, "  <task id=%q status=%q assigned=%q>%s</task>\n",
		xmlEsc(pkg.Task.ID), xmlEsc(pkg.Task.Status), xmlEsc(pkg.Task.AssignedTo), xmlEsc(pkg.Task.Title))
	if strings.TrimSpace(pkg.Task.Description) != "" {
		fmt.Fprintf(&b, "  <task_detail>%s</task_detail>\n", xmlEsc(pkg.Task.Description))
	}
	if len(pkg.Memories) > 0 {
		b.WriteString("  <session_memories>\n")
		for _, m := range pkg.Memories {
			fmt.Fprintf(&b, "    <memory key=%q>%s</memory>\n", xmlEsc(m.Key), xmlEsc(m.Content))
		}
		b.WriteString("  </session_memories>\n")
	}
	if len(pkg.ModifiedFiles) > 0 {
		b.WriteString("  <modified_files>\n")
		for _, f := range pkg.ModifiedFiles {
			fmt.Fprintf(&b, "    <file>%s</file>\n", xmlEsc(f))
		}
		b.WriteString("  </modified_files>\n")
	}
	fmt.Fprintf(&b, "  <branch name=%q commit=%q dirty=%v />\n",
		xmlEsc(pkg.Branch.Branch), xmlEsc(pkg.Branch.CommitSHA), pkg.Branch.IsDirty)
	fmt.Fprintf(&b, "  <handoff id=%q from=%q to=%q />\n", xmlEsc(pkg.ID), xmlEsc(pkg.FromUser), xmlEsc(pkg.ToUser))
	if strings.TrimSpace(pkg.Note) != "" {
		fmt.Fprintf(&b, "  <note>%s</note>\n", xmlEsc(pkg.Note))
	}
	b.WriteString("</handoff_context>")
	return b.String()
}

func renderInstructionFile(pkg *HandoffPackage, kind string) string {
	target := ".github/muse-instructions.md"
	header := "# Copilot Instructions — Session Handoff"
	if strings.Contains(kind, AgentCursor) {
		target = ".cursorrules"
		header = "# Cursor Rules — Session Handoff"
	}
	var b strings.Builder
	b.WriteString(header + "\n\n")
	b.WriteString("<!-- Auto-generated by nexus handoff " + pkg.ID + " — do not edit by hand; re-run handoff to refresh. -->\n\n")
	fmt.Fprintf(&b, "## Active task: %s `[%s]`\n\n", pkg.Task.Title, pkg.Task.Status)
	if strings.TrimSpace(pkg.Task.Description) != "" {
		b.WriteString(pkg.Task.Description + "\n\n")
	}
	fmt.Fprintf(&b, "Assignee after handoff: %s (was %s).\n\n", pkg.Task.AssignedTo, pkg.FromUser)
	if len(pkg.Memories) > 0 {
		b.WriteString("## Session context\n\n")
		for _, m := range pkg.Memories {
			fmt.Fprintf(&b, "- **%s**: %s\n", m.Key, m.Content)
		}
		b.WriteString("\n")
	}
	if len(pkg.ModifiedFiles) > 0 {
		b.WriteString("## Files in progress (coordinate before editing)\n\n")
		for _, f := range pkg.ModifiedFiles {
			b.WriteString("- `" + f + "`\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## Branch state\n\nBranch `%s` at `%s` (dirty=%v). Check out this branch before continuing.\n\n",
		pkg.Branch.Branch, pkg.Branch.CommitSHA, pkg.Branch.IsDirty)
	if strings.TrimSpace(pkg.Note) != "" {
		b.WriteString("## Handoff note\n\n" + pkg.Note + "\n\n")
	}
	b.WriteString("Target file: `" + target + "`\n")
	return b.String()
}

func renderMarkdown(pkg *HandoffPackage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Session handoff %s\n\n", pkg.ID)
	fmt.Fprintf(&b, "From %s to %s. Task: %s [%s].\n\n", pkg.FromUser, pkg.ToUser, pkg.Task.Title, pkg.Task.Status)
	for _, m := range pkg.Memories {
		fmt.Fprintf(&b, "- %s: %s\n", m.Key, m.Content)
	}
	if len(pkg.ModifiedFiles) > 0 {
		b.WriteString("\nFiles: " + strings.Join(pkg.ModifiedFiles, ", ") + "\n")
	}
	return b.String()
}
