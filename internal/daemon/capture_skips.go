package daemon

// Transcripts the harvester owns but does not read are recorded here, so a
// skip is never silent: each is logged once and listed in capture status.

import (
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SkippedTranscript is one owned transcript that capture does not read.
type SkippedTranscript struct {
	Agent  string    `json:"agent"`
	Path   string    `json:"path"`
	Name   string    `json:"name"`
	Bytes  int64     `json:"bytes"`
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

const maxSkippedTranscripts = 200

var skippedTranscripts = struct {
	sync.Mutex
	m map[string]SkippedTranscript
}{m: map[string]SkippedTranscript{}}

// fileTooBigToCapture reports whether a file is over the size cap for its
// format. JSONL is streamed from its saved offset and never too big; the
// OpenCode database is queried by SQL.
func fileTooBigToCapture(src TranscriptSource, path string, size int64) bool {
	if size <= maxWalkFileBytes {
		return false
	}
	switch src.Format {
	case FormatJSONL:
		return false
	case FormatSQLite:
		return !strings.EqualFold(filepath.Base(path), "opencode.db")
	default:
		return true
	}
}

// noteSkippedTranscript records (and logs, the first time) a transcript
// that is not read because of its size.
func noteSkippedTranscript(agent, path string, size int64) {
	if noteSkipped(agent, path, size, "larger than the 64 MB limit for this format") {
		log.Printf("capture: not reading %s transcript %s: %d MB is over the %d MB limit for this format",
			agent, path, size>>20, maxWalkFileBytes>>20)
	}
}

// noteSkipped records a skipped transcript and reports whether it is new.
func noteSkipped(agent, path string, size int64, reason string) bool {
	key := strings.ToLower(filepath.Clean(path))
	skippedTranscripts.Lock()
	_, known := skippedTranscripts.m[key]
	if !known && len(skippedTranscripts.m) < maxSkippedTranscripts {
		skippedTranscripts.m[key] = SkippedTranscript{
			Agent: agent, Path: path, Name: filepath.Base(path), Bytes: size,
			Reason: reason, Since: time.Now().UTC(),
		}
	} else if known {
		e := skippedTranscripts.m[key]
		e.Bytes = size
		skippedTranscripts.m[key] = e
	}
	skippedTranscripts.Unlock()
	return !known
}

// forgetSkipped drops a transcript from the list (it is handled again).
func forgetSkipped(path string) {
	skippedTranscripts.Lock()
	delete(skippedTranscripts.m, strings.ToLower(filepath.Clean(path)))
	skippedTranscripts.Unlock()
}

// SkippedTranscripts lists owned transcripts capture is not reading, largest
// first.
func SkippedTranscripts() []SkippedTranscript {
	skippedTranscripts.Lock()
	out := make([]SkippedTranscript, 0, len(skippedTranscripts.m))
	for _, e := range skippedTranscripts.m {
		out = append(out, e)
	}
	skippedTranscripts.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}
