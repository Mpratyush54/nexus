package daemon

// code_hunks_backfill.go replays the edits of this workspace's recent
// transcripts into the code-links reporter once, so line history covers
// chats the harvester read before edits were captured as hunks (its
// cursors are past them and it never reads them again).
//
// Bounded: JSONL transcripts modified in the last codeBackfillMaxAge, at
// most codeBackfillMaxFiles of them and codeBackfillMaxBytes read in all,
// newest first, streamed line by line. Hunks are located in the files as
// they are now: edits whose text has since changed are skipped. The server
// upserts hunks by (file, chat, tool call, hunk), so a rerun is harmless.
// A marker under the workspace's .central-memory folder records the run.

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	codeBackfillVersion   = "1"
	codeBackfillMarker    = "code-hunks-backfill"
	codeBackfillMaxAge    = 14 * 24 * time.Hour
	codeBackfillMaxFiles  = 40
	codeBackfillMaxProbe  = 400
	codeBackfillMaxBytes  = 768 << 20
	codeBackfillChunk     = 100
	codeBackfillStartWait = time.Minute
	// codeBackfillFlushBytes flushes the reporter before its edit queue
	// (maxPendingEditBytes) fills.
	codeBackfillFlushBytes = maxPendingEditBytes / 2
)

type backfillFile struct {
	path, agent string
	size        int64
	mod         time.Time
}

func codeBackfillMarkerPath(root string) string {
	return filepath.Join(root, tokenDirName, codeBackfillMarker)
}

// backfillCodeHunks runs the one-time replay for this runtime (see the
// file comment). It waits until the reporter is connected.
func (r *Runtime) backfillCodeHunks(ctx context.Context, rep *CodeLinksReporter) {
	if r == nil || rep == nil || r.Daemon == nil || r.Harvester == nil {
		return
	}
	root := r.Daemon.Root
	marker := codeBackfillMarkerPath(root)
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == codeBackfillVersion {
		return
	}
	wait := codeBackfillStartWait
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = 30 * time.Second
		if r.Paused() {
			continue
		}
		if _, _, projectID, _, err := rep.conn(); err == nil && looksLikeUUID(projectID) {
			break
		}
	}
	files := codeBackfillCandidates(r.Harvester, time.Now())
	notes, read := 0, int64(0)
	for _, f := range files {
		if ctx.Err() != nil || r.Paused() {
			return // not done: runs again next start
		}
		if read+f.size > codeBackfillMaxBytes {
			break
		}
		read += f.size
		notes += replayTranscriptEdits(ctx, rep, f, r.Paused)
		rep.Flush(ctx)
	}
	if ctx.Err() != nil || r.Paused() {
		return
	}
	_ = os.MkdirAll(filepath.Dir(marker), 0o700)
	_ = os.WriteFile(marker, []byte(codeBackfillVersion+"\n"), 0o600)
	log.Printf("daemon: code hunks backfill: %d transcripts, %d edit batches", len(files), notes)
}

// codeBackfillCandidates lists the recent JSONL transcripts h owns, newest
// first.
func codeBackfillCandidates(h *Harvester, now time.Time) []backfillFile {
	var all []backfillFile
	srcFor := map[string]TranscriptSource{}
	for _, src := range h.Sources {
		if src.Format != FormatJSONL || src.Global {
			continue
		}
		for _, dir := range src.Dirs {
			_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					// Sub-agent transcripts are not chats of their own.
					if strings.EqualFold(d.Name(), "subagents") {
						return filepath.SkipDir
					}
					return nil
				}
				if !strings.EqualFold(filepath.Ext(path), ".jsonl") {
					return nil
				}
				info, err := d.Info()
				if err != nil || now.Sub(info.ModTime()) > codeBackfillMaxAge {
					return nil
				}
				if _, dup := srcFor[path]; dup {
					return nil
				}
				srcFor[path] = src
				all = append(all, backfillFile{path: path, agent: src.Agent, size: info.Size(), mod: info.ModTime()})
				return nil
			})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	var out []backfillFile
	for i, f := range all {
		if i >= codeBackfillMaxProbe || len(out) >= codeBackfillMaxFiles {
			break
		}
		if h.MatchesTranscript(f.path, srcFor[f.path].CwdMatch) {
			out = append(out, f)
		}
	}
	return out
}

// replayTranscriptEdits streams one transcript and notes its turns that
// carry tool calls, in chunks. It returns how many chunks it noted.
func replayTranscriptEdits(ctx context.Context, rep *CodeLinksReporter, f backfillFile, paused func() bool) int {
	fh, err := os.Open(f.path)
	if err != nil {
		return 0
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64<<10), maxJSONLLineBytes)
	var chunk []Event
	notes := 0
	note := func() {
		if len(chunk) == 0 {
			return
		}
		native, _ := chunk[0].Payload["session_id"].(string)
		if strings.TrimSpace(native) != "" {
			rep.NoteEvents("", native, harnessFromEvents(chunk), chunk)
			notes++
		}
		chunk = nil
		if rep.queuedEditBytes() >= codeBackfillFlushBytes {
			rep.Flush(ctx)
		}
	}
	for sc.Scan() {
		if ctx.Err() != nil || (paused != nil && paused()) {
			return notes
		}
		line := sc.Bytes()
		// Cheap pre-filter: only lines that can carry an edit are parsed.
		if !mayCarryEdit(line) {
			continue
		}
		t, ok := parseTurnLine(line)
		if !ok || len(t.ToolCalls) == 0 {
			continue
		}
		p := turnPayload("backfill", f.agent, f.path, t)
		at := t.Timestamp
		if at.IsZero() {
			at = f.mod
		}
		chunk = append(chunk, Event{Type: EventConversationTurn, Payload: p, CreatedAt: at})
		if len(chunk) >= codeBackfillChunk {
			note()
		}
	}
	note()
	return notes
}

// mayCarryEdit reports whether a transcript line can hold an edit call.
func mayCarryEdit(line []byte) bool {
	for _, k := range []string{"old_string", "file_path", "apply_patch", "*** Begin Patch", "oldString", "filePath", "file_text", "TargetFile", "target_file", "code_edit", "+++ "} {
		if bytes.Contains(line, []byte(k)) {
			return true
		}
	}
	return false
}

// queuedEditBytes is the edit text waiting for a flush.
func (c *CodeLinksReporter) queuedEditBytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.editBytes
}
