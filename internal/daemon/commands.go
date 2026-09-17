// Allowlisted command execution for the workspace daemon (issue #3).
//
// POST /command/run executes ONLY the plan's v1 allowlist:
//
//	git <...>          any git subcommand (Dir-pinned, no shell)
//	go test <...>      Go tests
//	npm test <...>     npm tests
//	pytest <...>       Python tests
//	cargo test <...>   Rust tests
//
// Anything else → 400. Execution uses os/exec with argv (never a shell),
// Dir pinned to the workspace root, and a 60s timeout (→ 504 on expiry).
// Output is capped at 32KB with a truncation marker so a verbose test run
// cannot blow up the JSON response or daemon memory.
package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// CommandTimeout bounds every allowlisted command (plan §1.3: 60s).
const CommandTimeout = 60 * time.Second

// MaxCommandOutput caps captured stdout+stderr per run.
const MaxCommandOutput = 32 << 10

// CommandRequest is the POST /command/run body.
type CommandRequest struct {
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
}

// CommandResult is the POST /command/run response.
type CommandResult struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

// IsAllowedCommand reports whether (cmd, args) is on the v1 allowlist.
// Matching is exact on the binary name plus the required subcommand:
//
//	git            → always allowed (any argv; Dir-pinned, no shell)
//	go test ...    → args[0] must be "test"
//	npm test ...   → args[0] must be "test"
//	pytest ...     → always allowed (pytest runs only tests)
//	cargo test ... → args[0] must be "test"
//
// Notably rejected: `go run`, `npm install/exec`, `cargo run/build`,
// absolute paths ("/bin/git"), and shells (sh, cmd, powershell).
func IsAllowedCommand(cmd string, args []string) bool {
	switch cmd {
	case "git":
		return true
	case "go", "npm", "cargo":
		return len(args) > 0 && args[0] == "test"
	case "pytest":
		return true
	default:
		return false
	}
}

// RunCommand executes an allowlisted command in root with a 60s timeout,
// returning the exit code and capped combined output. Non-allowlisted
// commands return an *AllowlistError (handler maps to 400); timeouts
// surface as context.DeadlineExceeded (handler maps to 504).
func RunCommand(ctx context.Context, root, cmd string, args []string) (int, string, error) {
	if !IsAllowedCommand(cmd, args) {
		return 0, "", &AllowlistError{Cmd: cmd}
	}
	ctx, cancel := context.WithTimeout(ctx, CommandTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, cmd, args...)
	c.Dir = root
	out, err := c.CombinedOutput()
	capped := truncateOutput(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return 0, capped, ctx.Err()
	}
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			return 0, capped, err
		}
	}
	return code, capped, nil
}

// AllowlistError marks a rejected non-allowlisted command.
type AllowlistError struct{ Cmd string }

func (e *AllowlistError) Error() string { return "command not allowlisted: " + e.Cmd }

func truncateOutput(s string) string {
	if len(s) <= MaxCommandOutput {
		return s
	}
	return s[:MaxCommandOutput] + "\n... [truncated]"
}

func (d *Daemon) handleCommandRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req CommandRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Cmd = strings.TrimSpace(req.Cmd)
	if req.Cmd == "" {
		writeError(w, http.StatusBadRequest, "cmd is required")
		return
	}
	if !IsAllowedCommand(req.Cmd, req.Args) {
		writeError(w, http.StatusBadRequest, "command not allowlisted")
		return
	}
	code, out, err := RunCommand(r.Context(), d.Root, req.Cmd, req.Args)
	exitForEvent := code
	if err != nil {
		exitForEvent = -1
	}
	d.Interceptor.OnCommand(JoinCmdline(req.Cmd, req.Args), exitForEvent, []byte(out), nil)
	if req.Cmd == "git" {
		d.checkGitCommit()
	}
	if err != nil {
		if r.Context().Err() == context.DeadlineExceeded || err == context.DeadlineExceeded {
			writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "command timed out"})
			return
		}
		// Non-exit failures (binary missing, spawn error) are 500; the
		// command itself was allowlisted, the environment failed it.
		writeJSON(w, http.StatusInternalServerError, map[string]any{"exit_code": code, "output": out, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, CommandResult{ExitCode: code, Output: out})
}
