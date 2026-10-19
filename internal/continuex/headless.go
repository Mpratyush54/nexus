package continuex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// TurnExecutor runs one non-interactive turn of a locally installed agent.
// It is separate from Runner: Runner starts an external session, whereas the
// desktop needs the answer back in its own chat view.
type TurnExecutor interface {
	Turn(ctx context.Context, plan LaunchPlan, workspace string) (string, error)
}

// HeadlessExecutor runs supported native CLIs without opening a console.
// A command can take a while, but output is bounded and stdin is closed so an
// interactive prompt cannot leave an invisible process waiting forever.
type HeadlessExecutor struct{}

const maxHeadlessOutput = 2 << 20

func (HeadlessExecutor) Turn(ctx context.Context, plan LaunchPlan, workspace string) (string, error) {
	if plan.Mode != "here" || !plan.Native || len(plan.Argv) == 0 {
		return "", errors.New("this agent cannot continue inside Nexus")
	}
	if !SupportsHere(plan.Agent) {
		return "", fmt.Errorf("%s does not offer a supported headless reply in Nexus yet", plan.Agent)
	}
	if strings.TrimSpace(workspace) == "" {
		return "", errors.New("select the original workspace before continuing here")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		return "", errors.New("the original workspace is not available on this machine")
	}
	path, err := exec.LookPath(plan.Argv[0])
	if err != nil {
		return "", fmt.Errorf("%s is not installed or is not on PATH", plan.Argv[0])
	}
	turnCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(turnCtx, path, plan.Argv[1:]...)
	cmd.Dir = workspace
	cmd.Stdin = nil
	hideConsole(cmd)
	var stdout, stderr cappedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if turnCtx.Err() != nil {
			return "", fmt.Errorf("agent timed out: %w", turnCtx.Err())
		}
		problem := strings.TrimSpace(stderr.String())
		if len(problem) > 500 {
			problem = problem[:500] + "…"
		}
		if problem == "" {
			problem = err.Error()
		}
		return "", fmt.Errorf("agent could not continue: %s", problem)
	}
	if stdout.truncated {
		return "", errors.New("agent reply exceeded the in-app display limit; open its native session to read the full output")
	}
	if answer := readableAgentOutput(stdout.String()); answer != "" {
		return answer, nil
	}
	return "", errors.New("agent finished without a readable reply; check its native session")
}

// SupportsHere is deliberately narrower than native resume. Some vendors
// expose resume-by-ID for an interactive terminal but not a prompt/reply
// command that Nexus can safely host without opening a console.
func SupportsHere(agent string) bool {
	switch normalizeAgent(agent) {
	case "claude", "codex", "gemini", "opencode", "kimi":
		return true
	default:
		return false
	}
}

type cappedOutput struct {
	bytes.Buffer
	truncated bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if w.Len() < maxHeadlessOutput {
		remaining := maxHeadlessOutput - w.Len()
		if remaining > n {
			remaining = n
		}
		_, _ = w.Buffer.Write(p[:remaining])
		if remaining < n {
			w.truncated = true
		}
	} else if n > 0 {
		w.truncated = true
	}
	return n, nil
}

// The native CLIs use different JSONL envelopes. Prefer assistant text and
// never expose tool arguments or raw event JSON as a chat answer.
func readableAgentOutput(raw string) string {
	var messages []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) != nil {
			messages = append(messages, line)
			continue
		}
		typeName, _ := row["type"].(string)
		switch typeName {
		case "item.completed": // Codex --json
			item, _ := row["item"].(map[string]any)
			if itemType, _ := item["type"].(string); itemType == "agent_message" {
				if text, _ := item["text"].(string); strings.TrimSpace(text) != "" {
					messages = append(messages, text)
				}
			}
		case "result": // Claude stream-json
			if text, _ := row["result"].(string); strings.TrimSpace(text) != "" {
				messages = append(messages, text)
			}
		case "text", "message": // OpenCode JSON events
			part, _ := row["part"].(map[string]any)
			if text, _ := part["text"].(string); strings.TrimSpace(text) != "" {
				messages = append(messages, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(messages, "\n\n"))
}
