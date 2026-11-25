// Package continuex builds Continue launch plans (D2 / P3).
// Plan/Command return argv only. Optional Runner implementations may record
// or spawn; unit tests use Controllable / PATH-overridden ExecRunner so no
// real agent binary is required.
package continuex

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
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
	// Stdin, when set, is written to the process and then closed (Claude
	// messages with image content blocks). Never shown as a command.
	Stdin []byte `json:"-"`
}

// SeededPlan starts a fresh local chat with captured context. Captured vendor
// IDs are often from another machine and cannot be resumed by a local CLI.
func SeededPlan(agent, prompt, context string, images []string) (LaunchPlan, error) {
	return SeededPlanTrusted(agent, prompt, context, images, false)
}

// SeededPlanTrusted only adds Cursor's workspace-trust flag after the caller
// has received an explicit opt-in for the selected local directory.
func SeededPlanTrusted(agent, prompt, context string, images []string, trustWorkspace bool) (LaunchPlan, error) {
	agent = normalizeAgent(agent)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return LaunchPlan{}, errors.New("continue: prompt is required")
	}
	seed := prompt
	if context = SafeContinuationContext(context); context != "" {
		seed = context + "\n\nNew user message:\n" + prompt
	}
	var argv []string
	switch agent {
	case "opencode":
		argv = []string{"opencode", "run", "--format", "json"}
		for _, image := range images {
			argv = append(argv, "--file", image)
		}
		argv = append(argv, seed)
	case "cursor", "agent":
		if len(images) != 0 {
			return LaunchPlan{}, errors.New("Cursor CLI image attachments are not supported here")
		}
		argv = []string{"cursor-agent", "--disable-auto-update", "-p", "--output-format", "stream-json", "--stream-partial-output"}
		if trustWorkspace {
			argv = append(argv, "--trust")
		}
		argv = append(argv, seed)
	case "codex":
		argv = []string{"codex", "exec", "--json"}
		for _, image := range images {
			argv = append(argv, "--image", image)
		}
		argv = append(argv, seed)
	case "claude":
		if len(images) != 0 {
			// Claude Code -p has no image flag: the message, with base64
			// image content blocks, goes in as stream-json on stdin.
			message, err := ClaudeUserMessage(seed, images)
			if err != nil {
				return LaunchPlan{}, err
			}
			line, err := json.Marshal(message)
			if err != nil {
				return LaunchPlan{}, err
			}
			argv = []string{"claude", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
			return LaunchPlan{Agent: agent, Mode: "here", Native: false, Argv: argv, Stdin: append(line, byte(10))}, nil
		}
		argv = []string{"claude", "-p", seed, "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
	default:
		return LaunchPlan{}, errors.New("continue: no in-app chat driver for " + agent)
	}
	return LaunchPlan{Agent: agent, Mode: "here", Native: false, Argv: argv}, nil
}

// WithModel applies an explicit model choice without routing any argument
// through a shell. An empty model keeps the harness's own default.
func WithModel(plan LaunchPlan, model string) (LaunchPlan, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return plan, nil
	}
	if len(model) > 160 || strings.HasPrefix(model, "-") || strings.IndexFunc(model, unicode.IsSpace) >= 0 ||
		strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return LaunchPlan{}, errors.New("continue: invalid model name")
	}
	if normalizeAgent(plan.Agent) == "opencode" && !strings.Contains(model, "/") {
		return LaunchPlan{}, errors.New("OpenCode model must be provider/model")
	}
	if len(plan.Argv) == 0 {
		return LaunchPlan{}, errors.New("continue: empty launch plan")
	}
	last := len(plan.Argv) - 1
	argv := append([]string{}, plan.Argv[:last]...)
	argv = append(argv, "--model", model, plan.Argv[last])
	plan.Argv = argv
	return plan, nil
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
	if agent == "codex" && strings.HasPrefix(sessionNativeID, "rollout-") {
		// Rollout file names are not Codex thread IDs. Preserve the UUID suffix.
		base := strings.TrimSuffix(strings.TrimSuffix(sessionNativeID, ".jsonl"), ".json")
		if len(base) >= 36 {
			candidate := base[len(base)-36:]
			valid := true
			for i, c := range candidate {
				if i == 8 || i == 13 || i == 18 || i == 23 {
					valid = valid && c == '-'
				} else {
					valid = valid && strings.ContainsRune("0123456789abcdefABCDEF", c)
				}
			}
			if valid {
				sessionNativeID = candidate
			}
		}
	}
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
			argv = append(argv, "--output-format", "stream-json", "--verbose", "--include-partial-messages")
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
		bin := "cursor-agent"
		if headless {
			argv := []string{bin, "--disable-auto-update", "-p", "--resume", id, "--output-format", "stream-json", "--stream-partial-output", prompt}
			return argv, true, nil
		}
		return []string{bin, "--disable-auto-update", "--resume", id}, true, nil
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
			argv := []string{"opencode", "run", "-s", id, "--format", "json"}
			if prompt != "" {
				argv = append(argv, prompt)
			}
			return argv, true, nil
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
