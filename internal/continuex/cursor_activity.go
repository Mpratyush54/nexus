package continuex

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"central-memory/internal/secrets"
	"github.com/pmezard/go-difflib/difflib"
)

const maxCursorActivities = 100
const maxEditPreview = 128 << 10

type cursorActivityTracker struct {
	workspace string
	items     map[string]ToolActivity
	before    map[string]string
	changed   map[string]bool
}

// Cursor emits tool_call started/completed with a stable call_id. The
// installed CLI also emits edit deltas; don't mistake those for applied edits.
func (t *cursorActivityTracker) observe(line string) (ToolActivity, bool) {
	var event struct {
		Type    string                     `json:"type"`
		Subtype string                     `json:"subtype"`
		ID      string                     `json:"call_id"`
		Calls   map[string]json.RawMessage `json:"tool_call"`
	}
	if json.Unmarshal([]byte(line), &event) != nil || event.Type != "tool_call" ||
		(event.Subtype != "started" && event.Subtype != "completed") {
		return ToolActivity{}, false
	}
	var name string
	var call struct {
		Args      json.RawMessage            `json:"args"`
		Result    map[string]json.RawMessage `json:"result"`
		Name      string                     `json:"name"`
		Arguments json.RawMessage            `json:"arguments"`
	}
	// Recent CLIs include hook metadata alongside the tool and may serialize
	// protobuf oneofs as {case,value}. Accept both that and the documented
	// flattened format; unknown metadata must not discard the entire event.
	var toolValue json.RawMessage
	if raw, ok := event.Calls["tool"]; ok {
		var union struct {
			Case  string          `json:"case"`
			Value json.RawMessage `json:"value"`
		}
		if json.Unmarshal(raw, &union) == nil {
			name, toolValue = union.Case, union.Value
		}
	} else {
		for key, value := range event.Calls {
			if strings.HasSuffix(key, "ToolCall") || key == "function" {
				if name != "" {
					return ToolActivity{}, false
				}
				name, toolValue = key, value
			}
		}
	}
	if name == "" || json.Unmarshal(toolValue, &call) != nil {
		return ToolActivity{}, false
	}
	if raw, ok := call.Result["result"]; ok {
		var union struct {
			Case  string          `json:"case"`
			Value json.RawMessage `json:"value"`
		}
		if json.Unmarshal(raw, &union) == nil && union.Case != "" {
			call.Result = map[string]json.RawMessage{union.Case: union.Value}
		}
	}
	if name == "function" {
		name = call.Name
		call.Args = call.Arguments
	}
	var input map[string]any
	_ = json.Unmarshal(call.Args, &input)
	if input == nil {
		var text string
		if json.Unmarshal(call.Args, &text) == nil {
			_ = json.Unmarshal([]byte(text), &input)
		}
	}
	if event.ID == "" {
		event.ID, _ = input["toolCallId"].(string)
	}
	if event.ID == "" {
		_ = json.Unmarshal(event.Calls["toolCallId"], &event.ID)
	}
	// No fabricated identifiers: repeated uncorrelated events cannot safely
	// replace running tools or be attributed to a file edit.
	if event.ID == "" || len(event.ID) > 256 || name == "" {
		return ToolActivity{}, false
	}
	if t.items == nil {
		t.items = make(map[string]ToolActivity)
		t.before = make(map[string]string)
		t.changed = make(map[string]bool)
	}
	old, exists := t.items[event.ID]
	if !exists && len(t.items) >= maxCursorActivities {
		return ToolActivity{}, false
	}
	if exists && old.Status != "running" && event.Subtype == "started" {
		return ToolActivity{}, false
	}
	activity := ToolActivity{ID: event.ID, Tool: progressLabel(strings.TrimSuffix(name, "ToolCall"), 100), Status: "running"}
	if event.Subtype == "completed" {
		activity.Status = "completed"
	}
	part := openCodePart{}
	detailInput := make(map[string]any, len(input))
	for key, value := range input {
		// Full write payloads aren't useful as raw JSON. The completed
		// edit's Changes section is the readable, redacted representation.
		if key == "streamContent" || key == "fileText" || key == "fileBytes" {
			continue
		}
		detailInput[key] = value
	}
	part.State.Input, _ = json.Marshal(detailInput)
	populateOpenCodeToolDetails(&activity, part)
	if started, ended := cursorEventMillis(event.Calls["startedAtMs"]), cursorEventMillis(event.Calls["completedAtMs"]); started > 0 && ended >= started {
		activity.DurationMS = ended - started
	}
	if exists && len(activity.Files) == 0 {
		activity.Files = old.Files
	}
	if exists && activity.Command == "" {
		activity.Command = old.Command
	}
	if exists && activity.Input == "" {
		activity.Input = old.Input
	}
	path := ""
	if len(activity.Files) > 0 {
		path = activity.Files[0]
	}
	edit := name == "editToolCall" || name == "writeToolCall" || name == "deleteToolCall"
	if edit && event.Subtype == "started" && !exists {
		if full, ok := safeActivityPath(t.workspace, path); ok && !secrets.DetectSecretPath(path) {
			if text, ok := readEditPreview(full); ok {
				t.before[event.ID] = text
			}
		}
	}
	var success map[string]any
	if raw, ok := call.Result["success"]; ok {
		_ = json.Unmarshal(raw, &success)
	}
	for key, raw := range call.Result {
		switch key {
		case "success", "sandboxPolicy", "isBackground", "terminalsFolder", "pid":
			continue
		}
		activity.Status = "error"
		activity.Error = activityDetail(key+": "+string(raw), 2000)
		break
	}
	if len(success) > 0 {
		if content, ok := success["content"].(string); ok {
			activity.Output = activityDetail(content, 16000)
		} else {
			var outputCopy any
			_ = json.Unmarshal(call.Result["success"], &outputCopy)
			encoded, _ := json.MarshalIndent(redactActivityInput(outputCopy, 0), "", "  ")
			activity.Output = activityDetail(string(encoded), 16000)
		}
		if exit, ok := success["exitCode"].(float64); ok {
			value := int(exit)
			activity.ExitCode = &value
			if value != 0 {
				activity.Status = "error"
			}
		}
	}
	secretFile := false
	for _, file := range activity.Files {
		secretFile = secretFile || secrets.DetectSecretPath(file)
	}
	if secretFile {
		activity.Input = "Input details hidden because this tool accessed a sensitive file."
		activity.Output = "Output hidden because this tool accessed a sensitive file."
		activity.Error = activityDetail(activity.Status, 100)
	} else if edit && event.Subtype == "completed" && activity.Status == "completed" && success != nil {
		if full, ok := safeActivityPath(t.workspace, path); ok {
			rel, _ := filepath.Rel(t.workspace, full)
			t.changed[filepath.ToSlash(rel)] = true
			if diff, ok := success["diffString"].(string); ok && diff != "" {
				activity.Diff = activityDetail(diff, 16000)
			} else {
				before, known := t.before[event.ID]
				if text, ok := success["beforeFullFileContent"].(string); ok {
					before, known = text, true
				}
				after, readable := readEditPreview(full)
				if known && readable {
					activity.Diff = editPreview(path, before, after)
				}
			}
			// Full contents are represented by a bounded diff, not a huge
			// duplicate JSON document in the tool's output.
			activity.Output = "File change applied · " + progressLabel(path, 512)
		}
	}
	if event.Subtype == "completed" {
		delete(t.before, event.ID)
	}
	if exists && activity.Status == "completed" && activity.Diff == "" {
		activity.Diff = old.Diff
	}
	if exists && reflect.DeepEqual(old, activity) {
		return ToolActivity{}, false
	}
	t.items[event.ID] = activity
	return activity, true
}

// Never read outside the consented workspace, through symlinks, or inside a
// secret path. This applies to previews only; it doesn't grant harness access.
func safeActivityPath(root, path string) (string, bool) {
	if root == "" || path == "" {
		return "", false
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(base, path)
	}
	full, err = filepath.Abs(full)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) || secrets.DetectSecretPath(rel) {
		return "", false
	}
	current := base
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false
		}
	}
	return full, true
}

func readEditPreview(path string) (string, bool) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return "", true
	}
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxEditPreview {
		return "", false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEditPreview+1))
	return string(data), err == nil && len(data) <= maxEditPreview && utf8.Valid(data) && !strings.ContainsRune(string(data), 0)
}

func editPreview(path, before, after string) string {
	if before == after {
		return ""
	}
	if len(before) > maxEditPreview || len(after) > maxEditPreview || strings.Count(before, "\n") > 2000 || strings.Count(after, "\n") > 2000 {
		return "Diff preview omitted: file is too large."
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(before), B: difflib.SplitLines(after), FromFile: "before/" + path, ToFile: "after/" + path, Context: 3})
	if err != nil {
		return "Diff preview unavailable."
	}
	return activityDetail(diff, 16000)
}

func (t *cursorActivityTracker) changedPaths() []string {
	paths := make([]string, 0, len(t.changed))
	for path := range t.changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func mergeChangedPaths(groups ...[]string) []string {
	seen := map[string]bool{}
	for _, group := range groups {
		for _, path := range group {
			seen[path] = true
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func cursorEventMillis(raw json.RawMessage) int64 {
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return 0
	}
	value, _ := strconv.ParseInt(string(number), 10, 64)
	return value
}
