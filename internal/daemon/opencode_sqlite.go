package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// OpenCode keeps all projects and sessions in one live WAL-backed database.
// Read it in-process: requiring a separately installed sqlite3 executable
// left Windows capture silently empty and could flash a console window.
type openCodeSQLite struct {
	Root string
}

func (e *openCodeSQLite) ExtractNewRows(dbPath string, since time.Time) ([]Turn, error) {
	return readOpenCodeTurns(dbPath, e.Root, "", since, 400)
}

// The snapshot path asks for one native session, not every chat in the global
// database. A separate limit keeps long sessions usable without copying the DB.
func extractOpenCodeSessionTurns(dbPath, workspace, sessionID string) []Turn {
	turns, _ := readOpenCodeTurns(dbPath, workspace, sessionID, time.Time{}, 5000)
	return turns
}

func extractOpenCodeTurns(dbPath, workspace string, since time.Time) []Turn {
	turns, _ := readOpenCodeTurns(dbPath, workspace, "", since, 400)
	return turns
}

func readOpenCodeTurns(dbPath, workspace, wantedSession string, since time.Time, limitPerSession int) ([]Turn, error) {
	root := normalizeOpenCodeDirectory(workspace)
	if root == "" {
		return nil, nil
	}
	path := filepath.ToSlash(dbPath)
	if filepath.VolumeName(dbPath) != "" {
		path = "/" + path
	}
	uri := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT id, directory, title FROM session`)
	if err != nil {
		return nil, err
	}
	type session struct{ id, title string }
	var sessions []session
	for rows.Next() {
		var id, directory, title string
		if err := rows.Scan(&id, &directory, &title); err != nil {
			rows.Close()
			return nil, err
		}
		if normalizeOpenCodeDirectory(directory) == root && (wantedSession == "" || wantedSession == id) {
			sessions = append(sessions, session{id, title})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if limitPerSession < 1 {
		limitPerSession = 400
	}
	cutoff := since.UnixMilli()
	if since.IsZero() {
		cutoff = 0
	}
	var out []Turn
	var firstReadError error
	skipped := 0
	for _, session := range sessions {
		// The part_session_idx index isolates bad pages in a damaged global DB.
		// One broken session must not suppress all the other workspaces/chats.
		parts, err := db.QueryContext(ctx, `SELECT p.id, p.message_id, m.data, p.data, p.time_created
			FROM (SELECT id, message_id, data, time_created FROM part
			      WHERE session_id = ? AND time_created >= ?
			        AND json_extract(data, '$.type') IN ('text', 'tool')
			      ORDER BY time_created DESC, id DESC LIMIT ?) p
			JOIN message m ON m.id = p.message_id
			ORDER BY p.time_created ASC, p.id ASC`, session.id, cutoff, limitPerSession)
		if err != nil {
			if firstReadError == nil {
				firstReadError = err
			}
			skipped++
			continue
		}
		type message struct {
			turn Turn
			at   int64
		}
		byMessage := map[string]*message{}
		for parts.Next() {
			var partID, messageID, messageJSON, partJSON string
			var created int64
			if err := parts.Scan(&partID, &messageID, &messageJSON, &partJSON, &created); err != nil {
				continue
			}
			var header struct {
				Role string `json:"role"`
			}
			var part struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Tool  string `json:"tool"`
				State struct {
					Input map[string]any `json:"input"`
				} `json:"state"`
			}
			if json.Unmarshal([]byte(messageJSON), &header) != nil || json.Unmarshal([]byte(partJSON), &part) != nil {
				continue
			}
			entry := byMessage[messageID]
			if entry == nil {
				entry = &message{turn: Turn{
					Speaker: normalizeSpeaker(header.Role), SessionID: session.id,
					SessionTitle: redact(session.title), SourcePosition: openCodePosition(created, messageID),
				}, at: created}
				byMessage[messageID] = entry
			}
			if created < entry.at {
				entry.at = created
				entry.turn.SourcePosition = openCodePosition(created, messageID)
			}
			switch part.Type {
			case "text":
				text := strings.TrimSpace(part.Text)
				if text != "" && !strings.HasPrefix(text, "<system-reminder>") {
					if entry.turn.Content != "" {
						entry.turn.Content += "\n"
					}
					entry.turn.Content += text
				}
			case "tool":
				if part.Tool != "" {
					input := map[string]any{}
					for _, key := range []string{"path", "filePath", "file_path", "target_file", "command", "cmd", "cwd", "workdir", "working_directory"} {
						if value, ok := part.State.Input[key].(string); ok && value != "" {
							input[key] = redact(value)
						}
					}
					entry.turn.ToolCalls = append(entry.turn.ToolCalls, map[string]any{"name": part.Tool, "input": input})
				}
			}
		}
		if err := parts.Err(); err != nil {
			if firstReadError == nil {
				firstReadError = err
			}
			skipped++
		}
		parts.Close()
		for _, entry := range byMessage {
			if entry.turn.Content == "" && len(entry.turn.ToolCalls) == 0 {
				continue
			}
			if entry.at > 0 {
				if entry.at < 1e12 {
					entry.at *= 1000
				}
				entry.turn.Timestamp = time.UnixMilli(entry.at).UTC()
			}
			out = append(out, entry.turn)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].SourcePosition < out[j].SourcePosition
		}
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	if skipped > 0 {
		log.Printf("harvester: OpenCode read skipped %d of %d sessions due to database errors", skipped, len(sessions))
	}
	if len(out) == 0 && firstReadError != nil {
		return nil, fmt.Errorf("OpenCode database has no readable turns: %w", firstReadError)
	}
	return out, nil
}

func normalizeOpenCodeDirectory(path string) string {
	return strings.TrimRight(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), `\`, "/")), "/")
}

func openCodePosition(created int64, id string) int64 {
	if created < 1e12 {
		created *= 1000
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return created*100000 + int64(h.Sum32()%100000)
}
