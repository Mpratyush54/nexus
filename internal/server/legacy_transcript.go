package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"central-memory/internal/store"
)

// Legacy snapshots predate the agent-session turn store. Their transcript
// lives in session_snapshots.transcript_payload as gzipped JSONL whose shape
// depends on the harness that recorded it. These helpers give the read path a
// tolerant best-effort parse so Chat and Summary are not empty for archived
// sessions.

const (
	legacyTurnTextLimit = 4000
	legacyTurnLimit     = 2000
)

// legacySnapshotTurns loads and parses the latest snapshot transcript for a
// legacy session. It returns nil when no snapshot or transcript exists.
func (s *Server) legacySnapshotTurns(r *http.Request, row *store.AgentSession) []store.SessionTurn {
	ps := provenanceStore(s.Store)
	if ps == nil || row == nil {
		return nil
	}
	for _, id := range sessionContentIDs(row) {
		if !uuidLikeSessionID(id) {
			continue
		}
		snap, err := ps.GetLatestSnapshot(r.Context(), id)
		if err != nil || snap == nil || len(snap.TranscriptPayload) == 0 {
			continue
		}
		raw, err := gunzipSnapshot(snap.TranscriptPayload)
		if err != nil || len(raw) == 0 {
			continue
		}
		if turns := ParseTranscriptJSONL(raw); len(turns) > 0 {
			for i := range turns {
				turns[i].SessionID = row.ID
			}
			return turns
		}
	}
	return nil
}

func gunzipSnapshot(payload []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, 64<<20))
}

// ParseTranscriptJSONL parses one JSON object per line into preview turns.
// It accepts the shapes shipped by past capture pipelines: the flat
// {speaker, content} rows Nexus wrote itself, Claude-style {role, message},
// Claude Code's typed lines ({type: "user"|"assistant", message}), and the
// Codex envelope ({type: "response_item", payload}). Unrecognized lines are
// skipped; one malformed line never fails the whole transcript.
func ParseTranscriptJSONL(raw []byte) []store.SessionTurn {
	turns := make([]store.SessionTurn, 0, 64)
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var entry map[string]any
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		if turn, ok := transcriptTurn(entry); ok {
			turn.SourceAt = transcriptSourceTime(entry)
			if turn.SourceAt != nil {
				turn.At = *turn.SourceAt
			}
			turns = append(turns, turn)
		}
		if len(turns) >= legacyTurnLimit {
			break
		}
	}
	for i := range turns {
		turns[i].Idx = i
	}
	return turns
}

func transcriptTurn(entry map[string]any) (store.SessionTurn, bool) {
	// Codex envelope: unwrap the payload.
	if payload, ok := entry["payload"].(map[string]any); ok {
		entry = payload
	}
	if speaker := strings.TrimSpace(jsonText(entry["speaker"])); speaker != "" {
		return buildTranscriptTurn(speaker, jsonText(entry["content"]), transcriptToolCalls(entry)), true
	}
	role := strings.TrimSpace(jsonText(entry["role"]))
	kind := strings.TrimSpace(jsonText(entry["type"]))
	if role == "" {
		switch kind {
		case "user", "assistant", "tool":
			role = kind
		case "function_call":
			// Codex stores tool calls as their own envelope entry.
			return buildTranscriptTurn("tool", "", []map[string]any{{
				"name": jsonText(entry["name"]),
				"args": entry["arguments"],
			}}), true
		default:
			return store.SessionTurn{}, false
		}
	}
	message, _ := entry["message"].(map[string]any)
	if message == nil {
		message = entry
	}
	text, calls := transcriptMessage(message)
	if strings.TrimSpace(text) == "" && len(calls) == 0 {
		return store.SessionTurn{}, false
	}
	return buildTranscriptTurn(role, text, calls), true
}

func transcriptMessage(message map[string]any) (string, []map[string]any) {
	content, present := message["content"]
	if !present {
		if text := jsonText(message["text"]); text != "" {
			return text, nil
		}
		content = message["output"]
	}
	switch value := content.(type) {
	case string:
		return value, nil
	case []any:
		var parts []string
		calls := []map[string]any{}
		for _, item := range value {
			block, ok := item.(map[string]any)
			if !ok {
				if text := jsonText(item); text != "" {
					parts = append(parts, text)
				}
				continue
			}
			switch strings.TrimSpace(jsonText(block["type"])) {
			case "text", "input_text", "output_text":
				if text := jsonText(block["text"]); text != "" {
					parts = append(parts, text)
				}
			case "tool_use":
				calls = append(calls, map[string]any{
					"name": jsonText(block["name"]),
					"args": block["input"],
				})
			}
		}
		return strings.Join(parts, "\n"), calls
	case map[string]any:
		return transcriptMessage(value)
	default:
		return jsonText(content), nil
	}
}

func transcriptToolCalls(entry map[string]any) []map[string]any {
	items, ok := entry["tool_calls"].([]any)
	if !ok {
		return nil
	}
	calls := make([]map[string]any, 0, len(items))
	for _, item := range items {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name := strings.TrimSpace(jsonText(call["name"])); name != "" {
			calls = append(calls, call)
			continue
		}
		if function, ok := call["function"].(map[string]any); ok {
			if name := strings.TrimSpace(jsonText(function["name"])); name != "" {
				calls = append(calls, map[string]any{
					"name": name,
					"args": function["arguments"],
				})
			}
		}
	}
	if len(calls) == 0 {
		return nil
	}
	return calls
}

func buildTranscriptTurn(role, text string, calls []map[string]any) store.SessionTurn {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "human":
		role = "user"
	case "tool", "tool_result", "function":
		role = "tool"
	default:
		role = "assistant"
	}
	text = strings.TrimSpace(text)
	if chars := []rune(text); len(chars) > legacyTurnTextLimit {
		text = string(chars[:legacyTurnTextLimit]) + "…"
	}
	turn := store.SessionTurn{Role: role, TextPreview: text}
	if len(calls) > 0 {
		if encoded, err := json.Marshal(calls); err == nil {
			turn.ToolCalls = encoded
		}
	}
	return turn
}

func jsonText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
	}
	return ""
}
