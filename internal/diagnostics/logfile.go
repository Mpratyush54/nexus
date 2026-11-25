package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Local log files. The desktop app (C#) and the Go core write to the same
// folder with the same scheme, so "Open logs folder" shows both:
//
//	nexus-20261125.log, nexus-20261125.1.log  (desktop)
//	core-20261125.log,  core-20261125.1.log   (core)
//
// A file rolls over at a new UTC day or when it would pass MaxLogFileBytes;
// at most MaxLogFiles files per prefix are kept, newest first.
const (
	MaxLogFileBytes = 2 << 20
	MaxLogFiles     = 5
	ringLines       = MaxLogLines
)

// DefaultDir is the per-user Nexus data folder that holds logs/ and
// reports/: %LOCALAPPDATA%\Nexus on Windows, ~/.nexus elsewhere. A desktop
// started with NEXUS_PROFILE has already pointed LOCALAPPDATA at the
// profile, so the core follows it.
func DefaultDir() string {
	if runtime.GOOS == "windows" {
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			return filepath.Join(local, "Nexus")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".nexus")
	}
	return filepath.Join(os.TempDir(), "nexus")
}

// LogsDir is <base>/logs.
func LogsDir(base string) string { return filepath.Join(base, "logs") }

// RollingFile is an io.Writer over size- and day-rotated log files.
type RollingFile struct {
	mu       sync.Mutex
	dir      string
	prefix   string
	maxBytes int64
	maxFiles int
	now      func() time.Time
	header   func() string

	f    *os.File
	day  string
	size int64
}

// NewRollingFile writes <dir>/<prefix>-YYYYMMDD.log. header, when set, is
// written at the top of every new file (app version, OS, pid).
func NewRollingFile(dir, prefix string, maxBytes int64, maxFiles int, header func() string) *RollingFile {
	if maxBytes <= 0 {
		maxBytes = MaxLogFileBytes
	}
	if maxFiles <= 0 {
		maxFiles = MaxLogFiles
	}
	return &RollingFile{dir: dir, prefix: prefix, maxBytes: maxBytes, maxFiles: maxFiles, now: time.Now, header: header}
}

// SetClock replaces the clock (tests).
func (r *RollingFile) SetClock(now func() time.Time) {
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

func (r *RollingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	day := r.now().UTC().Format("20060102")
	if r.f != nil && (day != r.day || r.size+int64(len(p)) > r.maxBytes) {
		r.closeLocked(day != r.day)
	}
	if r.f == nil {
		if err := r.openLocked(day); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Close closes the current file.
func (r *RollingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// closeLocked closes the open file. When the day did not change the file
// is full: it moves aside to <prefix>-<day>.<n>.log so the next write starts
// a fresh <prefix>-<day>.log.
func (r *RollingFile) closeLocked(newDay bool) {
	_ = r.f.Close()
	r.f = nil
	if newDay {
		return
	}
	current := r.currentPath(r.day)
	for n := 1; n < 1000; n++ {
		target := filepath.Join(r.dir, fmt.Sprintf("%s-%s.%d.log", r.prefix, r.day, n))
		if _, err := os.Stat(target); os.IsNotExist(err) {
			_ = os.Rename(current, target)
			return
		}
	}
	_ = os.Remove(current)
}

func (r *RollingFile) currentPath(day string) string {
	return filepath.Join(r.dir, r.prefix+"-"+day+".log")
}

func (r *RollingFile) openLocked(day string) error {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	path := r.currentPath(day)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	r.f, r.day, r.size = f, day, 0
	if st != nil {
		r.size = st.Size()
	}
	if r.size >= r.maxBytes {
		// Opened a full file from an earlier run: roll it first.
		r.closeLocked(false)
		if f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
			return err
		}
		r.f, r.size = f, 0
	}
	pruneLogs(r.dir, r.prefix, r.maxFiles, path)
	if r.header != nil {
		if h := strings.TrimRight(r.header(), "\r\n"); h != "" {
			n, _ := r.f.WriteString(h + "\n")
			r.size += int64(n)
		}
	}
	return nil
}

// LogFiles returns the prefix's log files, newest first.
func LogFiles(dir, prefix string) []string {
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `-\d{8}(\.\d+)?\.log$`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type item struct {
		path string
		mod  time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !pattern.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].mod.Equal(items[j].mod) {
			return items[i].mod.After(items[j].mod)
		}
		return items[i].path > items[j].path
	})
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.path
	}
	return out
}

func pruneLogs(dir, prefix string, keep int, current string) {
	// The open file counts as one of the kept files.
	others := 0
	for _, path := range LogFiles(dir, prefix) {
		if path == current {
			continue
		}
		others++
		if others > keep-1 {
			_ = os.Remove(path)
		}
	}
}

// Ring keeps the newest log lines in memory for crash reports.
type Ring struct {
	mu    sync.Mutex
	lines []string
	max   int
}

// NewRing keeps up to n lines (MaxLogLines when n <= 0).
func NewRing(n int) *Ring {
	if n <= 0 {
		n = ringLines
	}
	return &Ring{max: n}
}

// Add appends one line.
func (r *Ring) Add(line string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.lines = append(r.lines, line)
	if over := len(r.lines) - r.max; over > 0 {
		r.lines = append([]string(nil), r.lines[over:]...)
	}
	r.mu.Unlock()
}

// Lines returns a copy, oldest first.
func (r *Ring) Lines() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

var componentPrefix = regexp.MustCompile(`^[A-Za-z][\w./-]{0,31}: `)

// LogWriter turns Go `log` output into Nexus log lines:
//
//	2026-11-25T10:41:05.007Z ERROR [core] sync: upload failed status=500 ref=7K3M9QW2HX4D
//
// Every line is redacted before it is written, so a token or an email that
// reaches log.Printf never lands on disk. Use it with log.SetFlags(0).
type LogWriter struct {
	Out    interface{ Write([]byte) (int, error) }
	Ring   *Ring
	Source string // "core"
	Now    func() time.Time
}

func (w *LogWriter) Write(p []byte) (int, error) {
	text := strings.TrimRight(string(p), "\r\n")
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	source := w.Source
	if source == "" {
		source = SourceCore
	}
	var b strings.Builder
	for _, raw := range strings.Split(text, "\n") {
		raw = strings.TrimRight(raw, "\r")
		if raw == "" {
			continue
		}
		line := FormatLogLine(now(), "", source, raw)
		w.Ring.Add(line)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if w.Out != nil && b.Len() > 0 {
		if _, err := w.Out.Write([]byte(b.String())); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// FormatLogLine builds one redacted, capped log line. An empty level is
// inferred from the message ("panic" → ERROR, "error"/"failed" → WARN).
func FormatLogLine(at time.Time, level, source, message string) string {
	message = strings.TrimSpace(message)
	if level == "" {
		level, message = inferLevel(message)
	}
	if !componentPrefix.MatchString(message) {
		message = source + ": " + message
	}
	line := at.UTC().Format("2006-01-02T15:04:05.000Z") + " " + level + " [" + source + "] " + Redact(message)
	return truncate(line, MaxLogLineChars)
}

func inferLevel(message string) (string, string) {
	for _, l := range []string{"ERROR", "WARN", "INFO", "DEBUG"} {
		if strings.HasPrefix(message, l+" ") || strings.HasPrefix(message, l+":") {
			return l, strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(message, l), ":"))
		}
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "panic"):
		return "ERROR", message
	case strings.Contains(lower, "error"), strings.Contains(lower, "failed"), strings.Contains(lower, "fail:"):
		return "WARN", message
	}
	return "INFO", message
}
