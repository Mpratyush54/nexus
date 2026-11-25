package continuex

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// commandForPlan resolves argv[0] to a native executable when possible and
// returns a configured exec.Cmd. Every Continue spawn must go through here so
// Windows never launches visible cmd.exe shims and consoles stay hidden.
func commandForPlan(ctx context.Context, plan LaunchPlan, lookPath func(string) (string, error)) (*exec.Cmd, error) {
	if !processSpawnAllowed() {
		return nil, fmt.Errorf("continue: process spawn is disabled")
	}
	return commandForInAppTurn(ctx, plan, lookPath)
}

// commandForInAppTurn is reachable only from the validated, explicit
// continue.here Send path. It never invokes a shell or opens a console.
func commandForInAppTurn(ctx context.Context, plan LaunchPlan, lookPath func(string) (string, error)) (*exec.Cmd, error) {
	if len(plan.Argv) == 0 {
		return nil, fmt.Errorf("continue: empty argv")
	}
	if lookPath == nil {
		lookPath = lookAgent
	}
	path, err := lookPath(plan.Argv[0])
	if err != nil {
		return nil, agentErrorf(plan.Agent, "not_installed", "continue: look path %q: %v", plan.Argv[0], err)
	}
	path, prefix, err := directExecutable(path, plan.Agent)
	if err != nil {
		return nil, AsAgentError(plan.Agent, "cannot_start", err)
	}
	cmd := exec.CommandContext(ctx, path, append(prefix, plan.Argv[1:]...)...)
	cmd.WaitDelay = 5 * time.Second
	if normalizeAgent(plan.Agent) == "cursor" || normalizeAgent(plan.Agent) == "agent" {
		hideCursorToolConsole(cmd)
	} else {
		hideConsole(cmd)
	}
	return cmd, nil
}
