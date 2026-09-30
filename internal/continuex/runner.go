package continuex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// Runner starts a LaunchPlan and returns an op_id.
// Implementations record or spawn; they must not require a real agent binary
// in unit tests (use Controllable / RecordingRunner, or ExecRunner with a
// PATH override to a fake binary).
type Runner interface {
	Start(ctx context.Context, plan LaunchPlan) (opID string, err error)
}

// Controllable is a test/double Runner that records argv and returns
// synthetic op IDs without spawning processes.
type Controllable struct {
	mu     sync.Mutex
	Calls  []LaunchPlan
	NextID string // optional fixed next op_id; empty generates op_<n>
	Fail   error  // if set, Start returns this error
	seq    atomic.Uint64
}

// Start records plan and returns an op_id (or Fail).
func (c *Controllable) Start(_ context.Context, plan LaunchPlan) (string, error) {
	if c == nil {
		return "", errors.New("continue: nil runner")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Fail != nil {
		return "", c.Fail
	}
	cp := plan
	if plan.Argv != nil {
		cp.Argv = append([]string(nil), plan.Argv...)
	}
	c.Calls = append(c.Calls, cp)
	if id := c.NextID; id != "" {
		c.NextID = ""
		return id, nil
	}
	n := c.seq.Add(1)
	return fmt.Sprintf("op_ctrl_%d", n), nil
}

// LastArgv returns the argv from the most recent Start, or nil.
func (c *Controllable) LastArgv() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.Calls) == 0 {
		return nil
	}
	return append([]string(nil), c.Calls[len(c.Calls)-1].Argv...)
}

// RecordingRunner is an alias for Controllable (records argv, no spawn).
type RecordingRunner = Controllable

// ExecRunner starts plan.Argv via exec.CommandContext. Tests should put a
// fake binary on PATH (or set LookPath) so no real agent is required.
type ExecRunner struct {
	// LookPath resolves the binary name; nil uses exec.LookPath.
	LookPath func(file string) (string, error)
	// Detach, when true, starts the process and returns without waiting.
	// Default true — Continue launches are fire-and-forget from the core.
	Wait bool
}

// Start runs argv[0] with the remaining args. Returns a fresh op_id.
func (e *ExecRunner) Start(ctx context.Context, plan LaunchPlan) (string, error) {
	if len(plan.Argv) == 0 {
		return "", errors.New("continue: empty argv")
	}
	bin := plan.Argv[0]
	look := exec.LookPath
	if e != nil && e.LookPath != nil {
		look = e.LookPath
	}
	path, err := look(bin)
	if err != nil {
		return "", fmt.Errorf("continue: look path %q: %w", bin, err)
	}
	cmd := exec.CommandContext(ctx, path, plan.Argv[1:]...)
	cmd.Env = os.Environ()
	opID := newOpID()
	if e != nil && e.Wait {
		if err := cmd.Run(); err != nil {
			return "", err
		}
		return opID, nil
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	// Reap in the background so we do not leak zombies in long-lived cores.
	go func() { _ = cmd.Wait() }()
	return opID, nil
}

func newOpID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("op_%d", atomic.AddUint64(&fallbackSeq, 1))
	}
	return "op_" + hex.EncodeToString(b[:])
}

var fallbackSeq uint64
