package diagnostics

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Report folders under <base>/reports:
//
//	held/     written when something crashed; waits for the person to
//	          choose Send or Don't send (or is released automatically when
//	          "Send crash reports automatically" is on).
//	pending/  agreed to be sent; uploaded with retry. <id>.json is sent
//	          anonymously, <id>.signed.json with the saved sign-in.
const (
	heldDir    = "held"
	pendingDir = "pending"
	signedExt  = ".signed.json"
	plainExt   = ".json"
)

// ReportsDir is <base>/reports.
func ReportsDir(base string) string { return filepath.Join(base, "reports") }

// HeldDir is <base>/reports/held.
func HeldDir(base string) string { return filepath.Join(ReportsDir(base), heldDir) }

// PendingDir is <base>/reports/pending.
func PendingDir(base string) string { return filepath.Join(ReportsDir(base), pendingDir) }

// OSString describes this machine for Report.OS ("windows amd64").
func OSString() string { return runtime.GOOS + " " + runtime.GOARCH }

// Config is what the core's crash capture needs.
type Config struct {
	Base    string // DefaultDir() unless the desktop passed one
	Version string // core build version
	Ring    *Ring  // recent log lines attached to crash reports
}

var (
	cfgMu sync.RWMutex
	cfg   Config
)

// Configure sets the process-wide crash capture settings.
func Configure(c Config) {
	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()
}

func current() Config {
	cfgMu.RLock()
	defer cfgMu.RUnlock()
	c := cfg
	if c.Base == "" {
		c.Base = DefaultDir()
	}
	return c
}

// PanicReport builds a normalised kind=crash source=core report for a
// recovered panic. where names the goroutine or call that panicked.
func PanicReport(where string, v any, stack []byte) Report {
	c := current()
	now := time.Now().UTC()
	r := Report{
		ReportID:   NewReportID(),
		Kind:       KindCrash,
		Source:     SourceCore,
		AppVersion: c.Version,
		OS:         OSString(),
		Message:    fmt.Sprintf("panic: %v", v),
		Stack:      string(stack),
		Logs:       c.Ring.Lines(),
		Timestamp:  &now,
		Context:    map[string]string{"via": where, "core_version": c.Version},
	}
	_ = Normalize(&r, now)
	return r
}

// Hold writes r to held/<report_id>.json for the desktop to offer.
func Hold(base string, r Report) (string, error) {
	return writeReport(HeldDir(base), r.ReportID+plainExt, r)
}

// Enqueue writes r to pending/ for upload. signed sends it with the
// saved sign-in; otherwise it goes anonymously. Writing the same report_id
// again replaces the earlier copy, so a report is queued at most once.
func Enqueue(base string, r Report, signed bool) (string, error) {
	_ = os.Remove(filepath.Join(PendingDir(base), r.ReportID+plainExt))
	_ = os.Remove(filepath.Join(PendingDir(base), r.ReportID+signedExt))
	name := r.ReportID + plainExt
	if signed {
		name = r.ReportID + signedExt
	}
	return writeReport(PendingDir(base), name, r)
}

func writeReport(dir, name string, r Report) (string, error) {
	if !reportIDPattern.MatchString(r.ReportID) {
		return "", fmt.Errorf("%w: report_id", ErrInvalid)
	}
	body, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

// recordPanic logs a recovered panic with its stack and holds a crash
// report. It returns the report ID ("" if the report could not be written).
func recordPanic(where string, v any, stack []byte) string {
	log.Printf("ERROR panic: %s: %v\n%s", where, v, strings.TrimSpace(string(stack)))
	r := PanicReport(where, v, stack)
	if _, err := Hold(current().Base, r); err != nil {
		log.Printf("ERROR diagnostics: could not write crash report: %v", err)
		return ""
	}
	return r.ReportID
}

// Capture is used as `defer diagnostics.Capture(dir, version)()` at the
// top of main: it records the panic, then re-panics so the exit code and
// the stderr stack stay unchanged.
func Capture(dir, version string) func() {
	return func() {
		v := recover()
		if v == nil {
			return
		}
		c := current()
		if dir != "" {
			c.Base = dir
		}
		if version != "" {
			c.Version = version
		}
		Configure(c)
		recordPanic("main", v, debug.Stack())
		panic(v)
	}
}

// Guard is used as `defer diagnostics.Guard("capture")()` at the top of a
// goroutine that runs inside the desktop app. A panic there would take the
// whole app down; Guard logs it with the stack, holds a crash report and
// lets the goroutine end instead.
func Guard(where string) func() {
	return func() {
		if v := recover(); v != nil {
			recordPanic(where, v, debug.Stack())
		}
	}
}

// Go runs fn on a new goroutine under Guard.
func Go(where string, fn func()) {
	go guarded(where, fn)
}

// guarded runs fn under Guard on the calling goroutine (Go's body).
func guarded(where string, fn func()) {
	defer Guard(where)()
	fn()
}

// RecordPanic is for callers that recover themselves (nx_call turns a
// panic into an error result). It returns the held report's ID.
func RecordPanic(where string, v any, stack []byte) string {
	return recordPanic(where, v, stack)
}
