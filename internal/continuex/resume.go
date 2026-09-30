// Package continuex builds Continue launch plans (D2 / P3).
// Plans return argv only — they never spawn agents.
package continuex

import (
	"errors"
	"strings"
)

// Request is continue.start (core / HTTP).
type Request struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"`   // here | open_in_agent
	Resume    string `json:"resume"` // native | fork | seeded
	Agent     string `json:"agent"`
	Prompt    string `json:"prompt"`
	NativeID  string `json:"native_id"` // harness-native session id when known
}

// LaunchPlan is the argv a driver would run for one harness + mode.
// Spawning is intentionally out of scope for this package.
type LaunchPlan struct {
	Agent  string   `json:"agent"`
	Mode   string   `json:"mode"` // here | open_in_agent
	Native bool     `json:"native"`
	Argv   []string `json:"argv"`
}

// NativeHarnesses are agents with documented resume-by-id CLIs (spec 5.1).
var NativeHarnesses = []string{
	"claude", "codex", "cursor", "agent", "gemini", "agy", "copilot",
	"opencode", "kimi", "hermes",
}

// Command returns the argv the Nexus core wraps for continue.start.
func Command(r Request) ([]string, error) {
	mode := strings.TrimSpace(r.Mode)
	resume := strings.TrimSpace(r.Resume)
	if resume == "" {
		resume = "native"
	}
	switch resume {
	case "native", "fork", "seeded":
	default:
		return nil, errors.New("continue: resume must be native, fork, or seeded")
	}
	sessionID := strings.TrimSpace(r.SessionID)
	if sessionID == "" {
		return nil, errors.New("continue: session is required")
	}
	switch mode {
	case "here":
		argv := []string{"nexus", "continue", "--here", "--session", sessionID, "--resume", resume}
		if p := strings.TrimSpace(r.Prompt); p != "" {
			argv = append(argv, "--prompt", p)
		}
		return argv, nil
	case "open_in_agent":
		agent := strings.TrimSpace(r.Agent)
		if agent == "" {
			return nil, errors.New("continue: open_in_agent requires an agent")
		}
		return []string{"nexus", "continue", "--agent", agent, "--session", sessionID, "--resume", resume}, nil
	default:
		return nil, errors.New("continue: mode must be here or open_in_agent")
	}
}

// Plan builds a per-harness LaunchPlan for open_in_agent or headless-here modes.
// sessionNativeID is the vendor session id (claude ses_…, etc.). prompt is
// used for headless "here" invocations. Does not spawn processes.
func Plan(agent, mode, sessionNativeID, prompt string) (LaunchPlan, error) {
	agent = normalizeAgent(agent)
	mode = strings.TrimSpace(mode)
	sessionNativeID = strings.TrimSpace(sessionNativeID)
	if agent == "" {
		return LaunchPlan{}, errors.New("continue: agent is required")
	}
	if sessionNativeID == "" {
		return LaunchPlan{}, errors.New("continue: native session id is required")
	}
	switch mode {
	case "here", "open_in_agent":
	default:
		return LaunchPlan{}, errors.New("continue: mode must be here or open_in_agent")
	}
	argv, native, err := harnessArgv(agent, mode, sessionNativeID, strings.TrimSpace(prompt))
	if err != nil {
		return LaunchPlan{}, err
	}
	return LaunchPlan{Agent: agent, Mode: mode, Native: native, Argv: argv}, nil
}

func normalizeAgent(agent string) string {
	a := strings.ToLower(strings.TrimSpace(agent))
	switch a {
	case "cursor-cli", "cursor_agent":
		return "agent"
	case "claude-code", "claude_code":
		return "claude"
	case "antigravity", "antigravity-cli":
		return "agy"
	case "github-copilot", "gh-copilot":
		return "copilot"
	default:
		return a
	}
}

func harnessArgv(agent, mode, id, prompt string) ([]string, bool, error) {
	headless := mode == "here"
	switch agent {
	case "claude":
		if headless {
			argv := []string{"claude", "-p", "--resume", id}
			if prompt != "" {
				argv = append(argv, prompt)
			} else {
				argv = append(argv, "…")
			}
			argv = append(argv, "--output-format", "stream-json")
			return argv, true, nil
		}
		return []string{"claude", "--resume", id}, true, nil
	case "codex":
		if headless {
			argv := []string{"codex", "exec", "resume", id}
			if prompt != "" {
				argv = append(argv, prompt)
			} else {
				argv = append(argv, "…")
			}
			argv = append(argv, "--json")
			return argv, true, nil
		}
		return []string{"codex", "resume", id}, true, nil
	case "agent", "cursor":
		bin := "agent"
		if headless {
			argv := []string{bin, "-p", "--resume", id, "--output-format", "stream-json"}
			return argv, true, nil
		}
		return []string{bin, "--resume", id}, true, nil
	case "gemini":
		if headless {
			argv := []string{"gemini", "-r", id}
			if prompt != "" {
				argv = append(argv, prompt)
			}
			return argv, true, nil
		}
		return []string{"gemini", "--resume", id}, true, nil
	case "agy":
		if headless {
			argv := []string{"agy", "-p", "--conversation", id, "--output-format", "stream-json"}
			return argv, true, nil
		}
		return []string{"agy", "--conversation", id}, true, nil
	case "copilot":
		if headless {
			return []string{"copilot", "--resume=" + id}, true, nil
		}
		return []string{"copilot", "--resume=" + id}, true, nil
	case "opencode":
		if headless {
			return []string{"opencode", "run", "-s", id, "--format", "json"}, true, nil
		}
		return []string{"opencode", "-s", id}, true, nil
	case "kimi":
		if headless {
			argv := []string{"kimi", "--session", id, "-p"}
			if prompt != "" {
				argv = append(argv, prompt)
			}
			return argv, true, nil
		}
		return []string{"kimi", "--session", id}, true, nil
	case "hermes":
		if headless {
			argv := []string{"hermes", "--resume", id, "-q"}
			if prompt != "" {
				argv = append(argv, prompt)
			}
			argv = append(argv, "--format", "stream-json")
			return argv, true, nil
		}
		return []string{"hermes", "--resume", id}, true, nil
	default:
		return nil, false, errors.New("continue: seeded or unsupported agent " + agent)
	}
}
