package continuex

import (
	"context"
	"os/exec"
)

type HarnessAvailability struct {
	Agent      string   `json:"agent"`
	Name       string   `json:"name"`
	Installed  bool     `json:"installed"`
	Executable string   `json:"executable,omitempty"`
	Prefix     []string `json:"prefix,omitempty"`
	InstallURL string   `json:"install_url,omitempty"`
	Problem    string   `json:"problem,omitempty"`
}

// DetectHarness is read-only: it must never start a CLI, installer, or shell.
func DetectHarness(agent string) HarnessAvailability { return detectHarness(agent, exec.LookPath) }
func detectHarness(agent string, look func(string) (string, error)) HarnessAvailability {
	agent = normalizeAgent(agent)
	result := HarnessAvailability{Agent: agent, Name: agent}
	binary := agent
	switch agent {
	case "codex":
		result.Name = "Codex CLI"
		result.InstallURL = "https://developers.openai.com/codex/cli/"
	case "opencode":
		result.Name = "OpenCode"
		result.InstallURL = "https://opencode.ai/docs/"
	case "claude":
		result.Name = "Claude Code"
		result.InstallURL = "https://code.claude.com/docs/en/setup"
	case "cursor", "agent":
		result.Name = "Cursor CLI"
		binary = "cursor-agent"
		result.InstallURL = "https://cursor.com/docs/cli/installation"
	case "gemini":
		result.Name = "Gemini CLI"
		result.InstallURL = "https://geminicli.com/docs/get-started/installation/"
	case "kimi":
		result.Name = "Kimi Code"
		result.InstallURL = "https://moonshotai.github.io/kimi-code/"
	case "copilot":
		result.InstallURL = "https://docs.github.com/en/copilot/how-tos/set-up/install-copilot-cli"
	}
	path, err := look(binary)
	if err != nil {
		result.Problem = result.Name + " is not installed or is not available to Nexus. Install its CLI, sign in, then restart Nexus to refresh PATH."
		return result
	}
	path, prefix, err := directExecutable(path, agent)
	if err != nil {
		result.Problem = err.Error()
		return result
	}
	result.Installed = true
	result.Executable = path
	result.Prefix = prefix
	return result
}

// Model discovery runs only after the user asks to refresh the model picker.
func HarnessModels(ctx context.Context, agent string) ([]string, error) {
	catalog, err := DiscoverHarnessModels(ctx, agent, false)
	return catalog.Models, err
}
