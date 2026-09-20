package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// openCodeSQLite extracts dialogue from OpenCode's opencode.db (session +
// message + part tables), filtered to sessions whose directory matches the
// bound workspace. Uses the sqlite3 CLI read-only (ADR-033: no CGO driver).
type openCodeSQLite struct {
	Root string
}

// ExtractNewRows implements SQLiteExtractor.
func (e *openCodeSQLite) ExtractNewRows(dbPath string, since time.Time) ([]Turn, error) {
	return extractOpenCodeTurns(dbPath, e.Root, since), nil
}

func extractOpenCodeTurns(dbPath, workspace string, since time.Time) []Turn {
	bin, err := exec.LookPath("sqlite3")
	if err != nil || strings.TrimSpace(bin) == "" {
		return nil
	}
	folder := filepath.Base(strings.TrimSpace(workspace))
	if folder == "" || folder == "." || folder == string(filepath.Separator) {
		return nil
	}
	// LIKE pattern; escape %/_ in folder name.
	like := "%" + escapeLike(folder) + "%"
	// Read-only URI avoids "database disk image is malformed" on live WAL DBs.
	uri := "file:" + filepath.ToSlash(dbPath) + "?mode=ro"
	sql := fmt.Sprintf(`
SELECT json_extract(m.data,'$.role'),
       json_extract(p.data,'$.text'),
       p.time_created
FROM part p
JOIN message m ON m.id = p.message_id
JOIN session s ON s.id = p.session_id
WHERE s.directory LIKE %s ESCAPE '\'
  AND json_extract(p.data,'$.type') = 'text'
  AND length(coalesce(json_extract(p.data,'$.text'),'')) >= 20
ORDER BY p.time_created DESC
LIMIT 1200;
`, quoteSQL(like))

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-separator", "\t", uri, sql)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// Retry without URI if the build lacks URI support.
		cmd2 := exec.CommandContext(ctx, bin, "-separator", "\t", dbPath, sql)
		out.Reset()
		errBuf.Reset()
		cmd2.Stdout = &out
		cmd2.Stderr = &errBuf
		if err2 := cmd2.Run(); err2 != nil {
			return nil
		}
	}
	sinceMs := int64(0)
	if !since.IsZero() {
		sinceMs = since.UnixMilli()
	}
	var turns []Turn
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	sc.Buffer(make([]byte, 64<<10), 2<<20)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		role := strings.TrimSpace(parts[0])
		text := strings.TrimSpace(parts[1])
		if text == "" || len(text) < 20 {
			continue
		}
		if strings.HasPrefix(text, "<system-reminder>") {
			continue
		}
		var ts time.Time
		if len(parts) >= 3 {
			var ms int64
			if _, err := fmt.Sscan(parts[2], &ms); err == nil && ms > 0 {
				if sinceMs > 0 && ms < sinceMs {
					continue
				}
				// OpenCode stores ms; tolerate seconds.
				if ms < 1e12 {
					ms *= 1000
				}
				ts = time.UnixMilli(ms).UTC()
			}
		}
		turns = append(turns, Turn{
			Speaker:   normalizeSpeaker(role),
			Content:   text,
			Timestamp: ts,
		})
	}
	// We queried DESC for recency; reverse to chronological for extract.
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	return turns
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func quoteSQL(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
