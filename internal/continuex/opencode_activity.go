package continuex

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode"
)

// Decode tool details separately from assistant prose and reasoning. Only
// bounded, redacted tool details may reach the local activity view.
type openCodePart struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"sessionID"`
	MessageID   string          `json:"messageID"`
	Type        string          `json:"type"`
	Text        string          `json:"text"`
	Error       json.RawMessage `json:"error"`
	Attempt     int             `json:"attempt"`
	textPresent bool
	Tool        string   `json:"tool"`
	Files       []string `json:"files"`
	State       struct {
		Status   string          `json:"status"`
		Input    json.RawMessage `json:"input"`
		Title    string          `json:"title"`
		Output   string          `json:"output"`
		Error    string          `json:"error"`
		Metadata struct {
			Output   string `json:"output"`
			Diff     string `json:"diff"`
			Exit     *int   `json:"exit"`
			ExitCode *int   `json:"exitCode"`
		} `json:"metadata"`
		Time struct{ Start, End int64 } `json:"time"`
	} `json:"state"`
}

// ToolActivity is a bounded, redacted local execution record, never assistant prose.
type ToolActivity struct {
	ID         string   `json:"id"`
	Tool       string   `json:"tool"`
	Status     string   `json:"status"`
	Files      []string `json:"files,omitempty"`
	Title      string   `json:"title,omitempty"`
	Command    string   `json:"command,omitempty"`
	Input      string   `json:"input,omitempty"`
	Output     string   `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
	ExitCode   *int     `json:"exit_code,omitempty"`
	DurationMS int64    `json:"duration_ms,omitempty"`
	Diff       string   `json:"diff,omitempty"`
	Parent     string   `json:"parent,omitempty"` // tool id of the sub-agent call this ran inside
}

type openCodeActivity = ToolActivity

func progressLabel(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	r := []rune(strings.TrimSpace(s))
	if len(r) > limit {
		r = r[:limit]
	}
	return string(r)
}

func (t *openCodeTextTracker) observeActivity(part openCodePart) {
	if part.ID == "" || t.ignore[part.MessageID] {
		return
	}
	// Part metadata can arrive before message.updated. Retain a bounded pending
	// shape until the role is known, never private reasoning content.
	hasText := part.textPresent || strings.TrimSpace(part.Text) != ""
	part.Text = ""
	part.textPresent = hasText
	role := t.roles[part.MessageID]
	if role == "" {
		if _, exists := t.activityParts[part.ID]; exists || len(t.activityParts) < 128 {
			t.activityParts[part.ID] = part
		}
		return
	}
	delete(t.activityParts, part.ID)
	if role != "assistant" {
		return
	}
	if part.Type == "reasoning" || part.Type == "text" {
		if hasText {
			if part.Type == "reasoning" && !t.active && t.callback != nil {
				t.callback(StreamProgress{Kind: "status", Text: "OpenCode is processing the request · waiting for a written reply…"})
			}
			t.active = true
		}
		return
	}
	activity := openCodeActivity{ID: part.ID}
	switch part.Type {
	case "tool":
		activity.Tool = progressLabel(part.Tool, 100)
		activity.Status = part.State.Status
		switch activity.Status {
		case "pending", "running", "completed", "error":
		default:
			return
		}
		if activity.Tool == "" {
			return
		}
		populateOpenCodeToolDetails(&activity, part)
	case "patch":
		activity.Tool, activity.Status = "Workspace changes", "completed"
		for _, path := range part.Files {
			if len(activity.Files) >= 100 {
				break
			}
			if path = progressLabel(path, 512); path != "" {
				activity.Files = append(activity.Files, path)
			}
		}
		if len(activity.Files) == 0 {
			return
		}
	case "step-finish":
		t.active = true
		return
	default:
		return
	}
	t.active = true
	old, exists := t.tools[part.ID]
	if exists && (old.Status == "completed" || old.Status == "error") && (activity.Status == "pending" || activity.Status == "running") {
		return
	}
	if exists && reflect.DeepEqual(old, activity) {
		return
	}
	if !exists {
		t.toolOrder = append(t.toolOrder, part.ID)
	}
	t.tools[part.ID] = activity
	if t.callback != nil {
		encoded, _ := json.Marshal(activity)
		t.callback(StreamProgress{Kind: "activity", Text: string(encoded)})
	}
}

// Polling uses the same tracker as SSE, so reconnecting never duplicates tools
// or replaces a multi-step reply with only its last message.
func (t *openCodeTextTracker) observeMessages(messages []openCodeMessage, previous string) {
	start := 0
	for i, message := range messages {
		if message.Info.ID == previous && previous != "" {
			start = i + 1
		}
	}
	for _, message := range messages[start:] {
		if t.ignore[message.Info.ID] || message.Info.Role != "assistant" {
			continue
		}
		t.roles[message.Info.ID] = message.Info.Role
		for _, part := range message.Parts {
			part.MessageID = message.Info.ID
			t.observeActivity(part)
			if part.Type == "text" && part.ID != "" {
				tracked := t.part(part.ID)
				tracked.message, tracked.valid = message.Info.ID, true
				// Keep poll snapshots separate from SSE deltas. A snapshot can
				// already include tokens whose delta is still queued locally.
				if len(part.Text) >= len(tracked.polled) {
					tracked.polled = part.Text
				}
			}
		}
	}
	// Older message APIs may omit part IDs. Keep their text as a fallback,
	// separate from identified SSE parts to avoid displaying it twice.
	t.snapshotText, _ = latestOpenCodeAnswer(messages, previous)
	t.report(t.answer(), false)
}

func (t *openCodeTextTracker) answer() string {
	if text := t.text(); strings.TrimSpace(text) != "" {
		return text
	}
	return t.snapshotText
}

func (t *openCodeTextTracker) toolSummary() string {
	if len(t.tools) == 0 {
		return ""
	}
	completed, failed, incomplete := 0, 0, 0
	for _, activity := range t.tools {
		switch activity.Status {
		case "completed":
			completed++
		case "error":
			failed++
		default:
			incomplete++
		}
	}
	return fmt.Sprintf("Nexus execution record — OpenCode ended without a written assistant reply.\n\nTool activity: %d completed, %d failed, %d not confirmed complete. Review the activity and workspace changes before retrying; work may already have been applied.", completed, failed, incomplete)
}
