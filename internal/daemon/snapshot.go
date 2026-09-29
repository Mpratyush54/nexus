package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/security"
	"central-memory/internal/store"
)

const maxSnapshotDiffBytes = 512 * 1024
const maxUntrackedFileBytes = 100 * 1024

// CollectSnapshot builds a SessionSnapshot for a conversation across harnesses.
func CollectSnapshot(root, conversationID, harness, machineID string) (*store.SessionSnapshot, error) {
	harness = normalizeHarness(harness)
	layout, err := ResolveSnapshotLayout(harness, conversationID, root)
	if err != nil {
		return nil, err
	}
	if layout.TranscriptFile == "" && layout.ArtifactDir == "" {
		return nil, fmt.Errorf("snapshot: harness %q has no layout for %s", harness, conversationID)
	}

	transcriptRaw, err := readSnapshotTranscript(layout, root)
	if err != nil {
		return nil, fmt.Errorf("snapshot: read transcript: %w", err)
	}
	if security.ContainsSecret(transcriptRaw) {
		if redacted, ok := RedactSecrets(string(transcriptRaw)); ok {
			transcriptRaw = []byte(redacted)
		}
	}
	gzTranscript, err := gzipBytes(transcriptRaw)
	if err != nil {
		return nil, err
	}

	artifacts, err := tarGzipArtifacts(layout)
	if err != nil {
		log.Printf("daemon: snapshot artifacts: %v", err)
		artifacts = nil
	}

	branch, commit, dirty, _, _ := GitStatus(root)
	diffText, diffTrunc, err := collectUncommittedDiff(root)
	if err != nil {
		log.Printf("daemon: snapshot git diff: %v", err)
	}
	if security.ContainsSecret([]byte(diffText)) {
		if redacted, ok := RedactSecrets(diffText); ok {
			diffText = redacted
		}
	}
	diffSize := len(diffText)
	gzDiff, err := gzipBytes([]byte(diffText))
	if err != nil {
		return nil, err
	}

	turnCount := bytes.Count(transcriptRaw, []byte("\n"))
	if len(transcriptRaw) > 0 && !bytes.HasSuffix(transcriptRaw, []byte("\n")) {
		turnCount++
	}

	return &store.SessionSnapshot{
		Harness:           harness,
		ConversationID:    conversationID,
		TurnCount:         turnCount,
		GitBranch:         branch,
		GitCommit:         commit,
		GitDirty:          dirty,
		UncommittedDiff:   gzDiff,
		DiffSizeBytes:     diffSize,
		DiffTruncated:     diffTrunc,
		TranscriptPayload: gzTranscript,
		ArtifactsBundle:   artifacts,
		SourceMachineID:   machineID,
	}, nil
}

func readSnapshotTranscript(layout SnapshotLayout, workspaceRoot string) ([]byte, error) {
	path := layout.TranscriptFile
	// Antigravity: prefer full sibling when caller pointed at truncated log.
	if layout.Harness == "antigravity" && path != "" {
		dir := filepath.Dir(path)
		full := filepath.Join(dir, "transcript_full.jsonl")
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			path = full
		} else {
			alt := filepath.Join(dir, "transcript.jsonl")
			if _, err := os.Stat(path); err != nil {
				path = alt
			}
		}
	}
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			return raw, nil
		}
	}
	// SQLite-only harnesses (Windsurf/Copilot/OpenCode/…): extract dialogue
	// into JSONL at collect time — no permanent sidecar required.
	if layout.SQLiteSource != "" {
		return extractSnapshotTranscriptJSONL(layout.Harness, layout.SQLiteSource, workspaceRoot)
	}
	if path != "" {
		return nil, fmt.Errorf("no transcript at %s", path)
	}
	return nil, fmt.Errorf("no transcript path")
}

func collectUncommittedDiff(root string) (string, bool, error) {
	if strings.TrimSpace(root) == "" {
		return "", false, nil
	}
	diff, err := runGit(root, "diff", "HEAD", "--", ".", ":!vendor", ":!node_modules", ":!*.lock", ":!dist")
	if err != nil {
		// empty repo / no commits
		diff = ""
	}
	var b strings.Builder
	b.WriteString(diff)
	untracked, _ := runGit(root, "ls-files", "--others", "--exclude-standard")
	for _, rel := range strings.Split(untracked, "\n") {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		full := filepath.Join(root, rel)
		st, err := os.Stat(full)
		if err != nil || st.IsDir() || st.Size() > maxUntrackedFileBytes {
			continue
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		b.WriteString("\n--- /dev/null\n+++ b/")
		b.WriteString(filepath.ToSlash(rel))
		b.WriteByte('\n')
		for _, line := range strings.Split(string(raw), "\n") {
			b.WriteString("+")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	out := b.String()
	trunc := false
	if len(out) > maxSnapshotDiffBytes {
		out = out[:maxSnapshotDiffBytes]
		trunc = true
	}
	return out, trunc, nil
}

func gzipBytes(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tarGzipArtifacts packs harness brain/custom state. Transcripts are stored
// separately in TranscriptPayload — skip them and ephemeral dirs.
func tarGzipArtifacts(layout SnapshotLayout) ([]byte, error) {
	brainDir := layout.ArtifactDir
	st, err := os.Stat(brainDir)
	if err != nil || !st.IsDir() {
		return nil, err
	}
	skipPrefixes := artifactSkipPrefixes(layout)
	transcriptRel := ""
	if layout.TranscriptFile != "" {
		if rel, err := filepath.Rel(brainDir, layout.TranscriptFile); err == nil && !strings.HasPrefix(rel, "..") {
			transcriptRel = filepath.ToSlash(rel)
		}
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err = filepath.Walk(brainDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(brainDir, path)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if relSlash == "." {
			return nil
		}
		for _, skip := range skipPrefixes {
			if relSlash == skip || strings.HasPrefix(relSlash, skip+"/") {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if transcriptRel != "" && relSlash == transcriptRel {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".md", ".txt", ".json", ".jsonl", ".sh", ".py", ".go", ".ts", ".tsx", ".js", ".yaml", ".yml", ".toml":
		default:
			return nil
		}
		// Cap individual artifact files (avoid packing huge jsonl dumps twice).
		if info.Size() > maxUntrackedFileBytes*20 {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		hdr := &tar.Header{Name: relSlash, Mode: 0o644, Size: int64(len(raw))}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = tw.Write(raw)
		return err
	})
	if err == nil {
		if aerr := appendSmallSQLiteArtifact(tw, layout, brainDir); aerr != nil {
			err = aerr
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func artifactSkipPrefixes(layout SnapshotLayout) []string {
	switch layout.Harness {
	case "antigravity":
		return []string{".system_generated"}
	case "cursor":
		// Transcripts packed separately; terminals are ephemeral local state.
		return []string{"agent-transcripts", "terminals"}
	default:
		return nil
	}
}

// ungzipBytes is used by restore.
func ungzipBytes(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}
