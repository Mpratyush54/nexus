package daemon

// Source times for Cursor agent transcripts.
//
// Cursor's ~/.cursor/projects/<slug>/agent-transcripts/<id>.jsonl records
// carry no timestamp field at all ({"role":"user","message":{"content":[...]}}).
// The only time in the file is the prompt header Cursor prepends to each user
// message:
//
//	<timestamp>Monday, Oct 19, 2026, 10:08 PM (UTC+5:30)</timestamp>
//	<user_query>...</user_query>
//
// User turns take that time; assistant/tool turns inherit the prompt they
// answer. A historical transcript with no header at all falls back to the
// file's modification time, which is an approximation but far closer than the
// upload time.

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// cursorPromptTimePat matches the leading prompt header of a Cursor user turn.
var cursorPromptTimePat = regexp.MustCompile(`^\s*<timestamp>\s*([^<]{1,80}?)\s*</timestamp>`)

var cursorPromptLayouts = []string{
	"Monday, Jan 2, 2006, 3:04 PM",
	"Monday, January 2, 2006, 3:04 PM",
	"Mon, Jan 2, 2006, 3:04 PM",
	"Monday, Jan 2, 2006, 15:04",
	"Monday, January 2, 2006, 15:04",
	"Jan 2, 2006, 3:04 PM",
}

// cursorHistoricalAge is how long a transcript must be untouched before its
// modification time may stand in for missing source times. A file written
// more recently may still be receiving the turns being read.
const cursorHistoricalAge = 5 * time.Minute

// cursorSourceLookbackBytes bounds the read before a resumed cursor when
// looking for the prompt time that the first new turns answer.
const cursorSourceLookbackBytes = 1 << 20

// cursorPromptTime parses the <timestamp> header Cursor puts at the start of
// a user message. ok=false when there is none or it does not parse.
func cursorPromptTime(content string) (time.Time, bool) {
	m := cursorPromptTimePat.FindStringSubmatch(content)
	if len(m) < 2 {
		return time.Time{}, false
	}
	text := strings.TrimSpace(m[1])
	loc := time.Local
	if i := strings.LastIndex(text, "("); i >= 0 && strings.HasSuffix(text, ")") {
		zone, ok := parseUTCOffset(text[i+1 : len(text)-1])
		if !ok {
			return time.Time{}, false
		}
		loc = zone
		text = strings.TrimSpace(text[:i])
	}
	for _, layout := range cursorPromptLayouts {
		if at, err := time.ParseInLocation(layout, text, loc); err == nil {
			if at.Year() < 2000 {
				return time.Time{}, false
			}
			return at.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseUTCOffset parses "UTC", "UTC+5:30", "UTC-7", "GMT+05:30".
func parseUTCOffset(s string) (*time.Location, bool) {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)
	switch {
	case strings.HasPrefix(upper, "UTC"), strings.HasPrefix(upper, "GMT"):
		s = strings.TrimSpace(s[3:])
	default:
		return nil, false
	}
	if s == "" {
		return time.UTC, true
	}
	sign := 1
	switch s[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return nil, false
	}
	hh, mm, _ := strings.Cut(s[1:], ":")
	h, err := strconv.Atoi(hh)
	if err != nil || h > 14 {
		return nil, false
	}
	m := 0
	if mm != "" {
		if m, err = strconv.Atoi(mm); err != nil || m > 59 {
			return nil, false
		}
	}
	return time.FixedZone("", sign*(h*3600+m*60)), true
}

// isCursorTranscript reports whether a JSONL path is a Cursor agent
// transcript (by registered agent, or by its agent-transcripts directory).
func isCursorTranscript(agent, path string) bool {
	if agent == "cursor" {
		return true
	}
	if agent != "" {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "agent-transcripts" {
			return true
		}
	}
	return false
}

// fillCursorSourceTimes dates turns that carry no source time. A turn
// inherits the latest dated turn before it (carry seeds that from earlier
// reads); turns ahead of the first dated one take its time; with nothing
// dated at all, fallback (the historical file's mtime, or zero) is used.
// It returns the latest real source time seen, to carry into the next read.
func fillCursorSourceTimes(turns []Turn, carry, fallback time.Time) time.Time {
	last := carry
	var first time.Time
	for i := range turns {
		if !turns[i].Timestamp.IsZero() {
			first = turns[i].Timestamp
			break
		}
	}
	for i := range turns {
		if !turns[i].Timestamp.IsZero() {
			last = turns[i].Timestamp
			continue
		}
		switch {
		case !last.IsZero():
			turns[i].Timestamp = last
		case !first.IsZero():
			turns[i].Timestamp = first
		case !fallback.IsZero():
			turns[i].Timestamp = fallback
		}
	}
	return last
}

// historicalMtime returns mtime when the file has been quiet for at least
// cursorHistoricalAge (and the time is plausible), else zero.
func historicalMtime(mtime, now time.Time) time.Time {
	if mtime.IsZero() || mtime.Year() < 2000 || now.Sub(mtime) < cursorHistoricalAge {
		return time.Time{}
	}
	return mtime.UTC()
}

// sourceTimeBefore finds the latest turn source time in the bytes just
// before off, so a resumed read can date the turns that follow it.
func sourceTimeBefore(f *os.File, off int64) time.Time {
	start := off - cursorSourceLookbackBytes
	if start < 0 {
		start = 0
	}
	buf := make([]byte, off-start)
	read, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return time.Time{}
	}
	var last time.Time
	for _, line := range bytes.Split(buf[:read], []byte{'\n'}) {
		if t, ok := parseTurnLine(line); ok && !t.Timestamp.IsZero() {
			last = t.Timestamp
		}
	}
	return last
}
