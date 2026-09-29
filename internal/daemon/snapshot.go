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

// CollectSnapshot builds a SessionSnapshot for a conversation (Antigravity-first).
func CollectSnapshot(root, conversationID, harness, machineID string) (*store.SessionSnapshot, error) {
	harness = strings.TrimSpace(harness)
	if harness == "" {
		harness = "antigravity"
	}
	transcriptDir, brainDir := SnapshotHarnessPaths(harness, conversationID)
	if transcriptDir == "" && brainDir == "" {
		return nil, fmt.Errorf("snapshot: harness %q not supported yet (Antigravity only)", harness)
	}

	transcriptPath := filepath.Join(transcriptDir, "transcript.jsonl")
	transcriptRaw, err := os.ReadFile(transcriptPath)
	if err != nil {
		// Fall back to transcript_full.jsonl
		alt := filepath.Join(transcriptDir, "transcript_full.jsonl")
		transcriptRaw, err = os.ReadFile(alt)
		if err != nil {
			return nil, fmt.Errorf("snapshot: read transcript: %w", err)
		}
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

	artifacts, err := tarGzipBrain(brainDir)
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

// SnapshotHarnessPaths returns transcript log dir and brain root for a harness.
// Only Antigravity is implemented initially ([Audit Fix M5]).
func SnapshotHarnessPaths(harness, conversationID string) (transcriptDir, brainDir string) {
	harness = strings.ToLower(strings.TrimSpace(harness))
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", ""
	}
	switch harness {
	case "antigravity", "gemini", "":
		home, _ := os.UserHomeDir()
		brainDir = filepath.Join(home, ".gemini", "antigravity", "brain", conversationID)
		transcriptDir = filepath.Join(brainDir, ".system_generated", "logs")
		return transcriptDir, brainDir
	default:
		log.Printf("daemon: snapshot harness %q not implemented yet", harness)
		return "", ""
	}
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

func tarGzipBrain(brainDir string) ([]byte, error) {
	st, err := os.Stat(brainDir)
	if err != nil || !st.IsDir() {
		return nil, err
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
		if strings.HasPrefix(relSlash, ".system_generated/") || relSlash == ".system_generated" {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".md", ".txt", ".json", ".sh", ".py", ".go", ".ts", ".tsx", ".js":
		default:
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
	_ = tw.Close()
	_ = gz.Close()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
