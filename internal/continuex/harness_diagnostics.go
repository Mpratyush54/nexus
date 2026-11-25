package continuex

import (
	"io"
	"strings"
)

// Watch diagnostics while the process runs, including stderr and trailing
// messages without a newline. Cancellation ends the retrying process; output
// copying still completes normally so os/exec can reap it cleanly.
type harnessDiagnosticOutput struct {
	destination io.Writer
	agent       string
	stderr      bool
	pending     string
	onFailure   func(error)
	onProgress  func(StreamProgress)
	failed      bool
	retries     int
	lastStatus  string
}

func (w *harnessDiagnosticOutput) ReadFrom(r io.Reader) (int64, error) {
	return copyOutputChunks(r, w.Write)
}
func (w *harnessDiagnosticOutput) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *harnessDiagnosticOutput) Write(p []byte) (int, error) {
	// Inspect before forwarding: structured failures are never assistant text.
	w.pending += string(p)
	for {
		end := strings.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		w.check(w.pending[:end], true)
		w.pending = w.pending[end+1:]
	}
	if len(w.pending) <= 64*1024 {
		w.check(w.pending, false)
	} else {
		w.pending = ""
	}
	return w.destination.Write(p)
}

func (w *harnessDiagnosticOutput) check(line string, complete bool) {
	if w.failed {
		return
	}
	err := streamHarnessProblem(w.agent, line, w.stderr)
	if err == nil {
		if status, problem, found := streamRetryHealth(w.agent, strings.TrimSpace(line), w.stderr); found {
			err = problem
			if complete {
				w.retries++
				if w.retries > maxProviderRetries {
					err = &harnessFailure{agent: w.agent, kind: "retry"}
				}
			}
			if err == nil && status != w.lastStatus && w.onProgress != nil {
				w.lastStatus = status
				w.onProgress(StreamProgress{Kind: "status", Text: status})
			}
		}
	}
	if err != nil {
		w.failed = true
		if w.onFailure != nil {
			w.onFailure(err)
		}
	}
}
