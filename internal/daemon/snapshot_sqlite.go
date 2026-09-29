package daemon

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxSnapshotSQLiteArtifactBytes caps optional opaque DB packing into
// ArtifactsBundle. Large stores (opencode.db, fat workspace DBs) are never
// copied — transcript extraction is the primary teleport path.
const maxSnapshotSQLiteArtifactBytes = 2 << 20 // 2 MiB

// extractSnapshotTranscriptJSONL materializes dialogue from a SQLite store
// into JSONL bytes for SessionSnapshot.TranscriptPayload. Reuses the harvest
// SQLiteExtractor seam / CLI fallback (ADR-033: no mandatory Go SQL driver).
// No permanent sidecar is written — extraction is collect-time only.
func extractSnapshotTranscriptJSONL(harness, dbPath, workspaceRoot string) ([]byte, error) {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" {
		return nil, fmt.Errorf("snapshot: empty sqlite path")
	}
	if st, err := os.Stat(dbPath); err != nil || st.IsDir() {
		return nil, fmt.Errorf("snapshot: sqlite not found: %s", dbPath)
	}
	harness = normalizeHarness(harness)
	turns := extractSnapshotTurns(harness, dbPath, workspaceRoot)
	if len(turns) == 0 {
		return nil, fmt.Errorf("snapshot: no turns extracted from %s", filepath.Base(dbPath))
	}
	return turnsToSnapshotJSONL(turns)
}

func extractSnapshotTurns(harness, dbPath, workspaceRoot string) []Turn {
	// OpenCode: dedicated schema-aware CLI extractor (same as harvest seam).
	if harness == "opencode" || strings.EqualFold(filepath.Base(dbPath), "opencode.db") {
		if turns := extractOpenCodeTurns(dbPath, workspaceRoot, time.Time{}); len(turns) > 0 {
			return turns
		}
		if strings.TrimSpace(workspaceRoot) != "" {
			ex := &openCodeSQLite{Root: workspaceRoot}
			if turns, err := ex.ExtractNewRows(dbPath, time.Time{}); err == nil && len(turns) > 0 {
				return turns
			}
		}
	}
	// Cursor/Copilot/Windsurf/Antigravity VS Code–style stores: CLI dump or
	// raw printable-string scan (issue #77 / sqlite_fallback.go).
	return extractSQLiteFallback(dbPath, time.Time{})
}

func turnsToSnapshotJSONL(turns []Turn) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	n := 0
	for _, t := range turns {
		content := strings.TrimSpace(t.Content)
		if content == "" {
			continue
		}
		speaker := strings.TrimSpace(t.Speaker)
		if speaker == "" {
			speaker = "unknown"
		}
		row := map[string]any{
			"speaker": speaker,
			"content": content,
		}
		if !t.Timestamp.IsZero() {
			row["timestamp"] = t.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		if err := enc.Encode(row); err != nil {
			return nil, err
		}
		n++
	}
	if n == 0 {
		return nil, fmt.Errorf("snapshot: empty jsonl after turn encode")
	}
	return buf.Bytes(), nil
}

// appendSmallSQLiteArtifact packs layout.SQLiteSource into the artifacts tar
// when the file is small enough. Prefer transcript extraction; this is an
// optional opaque companion for native IDE reopen on the destination machine.
func appendSmallSQLiteArtifact(tw *tar.Writer, layout SnapshotLayout, brainDir string) error {
	if tw == nil {
		return nil
	}
	p := strings.TrimSpace(layout.SQLiteSource)
	if p == "" {
		return nil
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() || st.Size() <= 0 || st.Size() > maxSnapshotSQLiteArtifactBytes {
		return nil
	}
	name := filepath.Base(p)
	if brainDir != "" {
		if rel, err := filepath.Rel(brainDir, p); err == nil && !strings.HasPrefix(rel, "..") {
			name = filepath.ToSlash(rel)
		}
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(raw))}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, bytes.NewReader(raw))
	return err
}
