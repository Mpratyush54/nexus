package continuex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// HarnessAvailability says whether an agent can run chats on this computer.
// Problem is a plain sentence for people; Detail is technical and only shown
// in an "Advanced details" expander. State is ready, not_installed,
// cannot_start or signed_out. SignedIn is yes, no or unknown (only checked
// cheaply, from credential files and environment variables).
type HarnessAvailability struct {
	Agent      string   `json:"agent"`
	Name       string   `json:"name"`
	Installed  bool     `json:"installed"`
	Usable     bool     `json:"usable"`
	State      string   `json:"state"`
	SignedIn   string   `json:"signed_in"`
	Executable string   `json:"executable,omitempty"`
	Prefix     []string `json:"prefix,omitempty"`
	InstallURL string   `json:"install_url,omitempty"`
	Problem    string   `json:"problem,omitempty"`
	Detail     string   `json:"detail,omitempty"`
}

// ChatAgents are the agents Nexus can run chats with, in picker order.
var ChatAgents = []string{"claude", "codex", "cursor", "opencode", "gemini", "kimi"}

// DetectHarness is read-only: it must never start a CLI, installer, or shell.
func DetectHarness(agent string) HarnessAvailability {
	home, _ := os.UserHomeDir()
	return withSignIn(detectHarness(agent, lookAgent), home, os.Getenv)
}

// DetectHarnesses checks every chat agent at once. Read-only and cheap: PATH
// lookups, file stats and launcher reads; no agent is started.
func DetectHarnesses() []HarnessAvailability {
	out := make([]HarnessAvailability, len(ChatAgents))
	var wg sync.WaitGroup
	for i, agent := range ChatAgents {
		wg.Add(1)
		go func(i int, agent string) {
			defer wg.Done()
			out[i] = DetectHarness(agent)
		}(i, agent)
	}
	wg.Wait()
	return out
}

func withSignIn(result HarnessAvailability, home string, env func(string) string) HarnessAvailability {
	if !result.Usable {
		return result
	}
	result.SignedIn = signedIn(result.Agent, home, env)
	if result.SignedIn == "no" {
		result.Usable = false
		result.State = "signed_out"
		result.Problem = (&AgentError{Agent: result.Agent, Kind: "signed_out"}).Message()
		result.Detail = "No sign-in was found. To sign in, " + signInHint(result.Agent)
	}
	return result
}

func detectHarness(agent string, look func(string) (string, error)) HarnessAvailability {
	agent = normalizeAgent(agent)
	result := HarnessAvailability{Agent: agent, Name: AgentDisplayName(agent), SignedIn: "unknown"}
	binary := agentBinary(agent)
	switch agent {
	case "codex":
		result.InstallURL = "https://developers.openai.com/codex/cli/"
	case "opencode":
		result.InstallURL = "https://opencode.ai/docs/"
	case "claude":
		result.InstallURL = "https://code.claude.com/docs/en/setup"
	case "cursor", "agent":
		result.InstallURL = "https://cursor.com/docs/cli/installation"
	case "gemini":
		result.InstallURL = "https://geminicli.com/docs/get-started/installation/"
	case "kimi":
		result.InstallURL = "https://moonshotai.github.io/kimi-code/"
	case "copilot":
		result.InstallURL = "https://docs.github.com/en/copilot/how-tos/set-up/install-copilot-cli"
	}
	path, err := look(binary)
	if err != nil {
		result.State = "not_installed"
		result.Problem = (&AgentError{Agent: agent, Kind: "not_installed"}).Message()
		result.Detail = `No "` + binary + `" command was found on PATH or in the usual install folders (npm, pnpm, bun, scoop, Volta, Chocolatey).`
		return result
	}
	exe, prefix, err := directExecutable(path, agent)
	if err != nil {
		result.State = "cannot_start"
		result.Installed = true
		result.Problem = (&AgentError{Agent: agent, Kind: "cannot_start"}).Message()
		result.Detail = "Found " + path + ", but no program to run behind it. Reinstalling " + result.Name + " usually fixes this."
		return result
	}
	result.Installed = true
	result.Usable = true
	result.State = "ready"
	result.Executable = exe
	result.Prefix = prefix
	return result
}

// signedIn reports "yes" when a credential the agent reads is present, "no"
// only when the agent certainly can't run without one and none is visible,
// and "unknown" otherwise (keychains, free models, unfamiliar setups).
func signedIn(agent, home string, env func(string) string) string {
	has := func(names ...string) bool {
		for _, n := range names {
			if strings.TrimSpace(env(n)) != "" {
				return true
			}
		}
		return false
	}
	file := func(parts ...string) bool {
		if len(parts) == 0 || parts[0] == "" {
			return false
		}
		info, err := os.Stat(filepath.Join(parts...))
		return err == nil && info.Mode().IsRegular() && info.Size() > 0
	}
	switch agent {
	case "codex":
		if has("OPENAI_API_KEY", "CODEX_API_KEY") || file(env("CODEX_HOME"), "auth.json") || file(home, ".codex", "auth.json") {
			return "yes"
		}
		return "no"
	case "gemini":
		if has("GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_GENAI_USE_GCA", "GOOGLE_APPLICATION_CREDENTIALS", "CLOUD_SHELL") ||
			file(home, ".gemini", "oauth_creds.json") {
			return "yes"
		}
		for _, f := range [][]string{{home, ".gemini", ".env"}, {home, ".env"}} {
			if f[0] == "" {
				continue
			}
			if raw, err := readSmall(filepath.Join(f...)); err == nil &&
				(strings.Contains(string(raw), "GEMINI_API_KEY") || strings.Contains(string(raw), "GOOGLE_API_KEY")) {
				return "yes"
			}
		}
		if geminiAuthType(home) != "" {
			return "unknown" // e.g. compute-default-credentials: can't check cheaply
		}
		return "no"
	case "claude":
		if has("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX") ||
			file(home, ".claude", ".credentials.json") {
			return "yes"
		}
		return "unknown" // macOS keeps the sign-in in the keychain
	case "opencode":
		if file(env("XDG_DATA_HOME"), "opencode", "auth.json") || file(home, ".local", "share", "opencode", "auth.json") {
			return "yes"
		}
		return "unknown" // OpenCode has free models that need no sign-in
	case "kimi":
		if has("KIMI_API_KEY", "MOONSHOT_API_KEY") {
			return "yes"
		}
		if home != "" {
			if hits, _ := filepath.Glob(filepath.Join(home, ".kimi-code", "credentials", "*.json")); len(hits) > 0 {
				return "yes"
			}
		}
		return "unknown"
	case "cursor", "agent":
		if has("CURSOR_API_KEY") {
			return "yes"
		}
		return "unknown"
	}
	return "unknown"
}

// geminiAuthType is the sign-in method chosen in ~/.gemini/settings.json
// (security.auth.selectedType, or the older selectedAuthType).
func geminiAuthType(home string) string {
	if home == "" {
		return ""
	}
	raw, err := readSmall(filepath.Join(home, ".gemini", "settings.json"))
	if err != nil {
		return ""
	}
	text := string(raw)
	for _, key := range []string{`"selectedType"`, `"selectedAuthType"`} {
		i := strings.Index(text, key)
		if i < 0 {
			continue
		}
		rest := strings.TrimLeft(text[i+len(key):], " \t\r\n:")
		if strings.HasPrefix(rest, `"`) {
			if k := strings.Index(rest[1:], `"`); k > 0 {
				return rest[1 : k+1]
			}
		}
	}
	return ""
}

func signInHint(agent string) string {
	switch agent {
	case "codex":
		return `run "codex login" in a terminal, or set OPENAI_API_KEY.`
	case "gemini":
		return `run "gemini" once in a terminal and choose a sign-in method, or set GEMINI_API_KEY.`
	}
	return "open the agent once in a terminal and sign in."
}

// Model discovery runs only after the user asks to refresh the model picker.
func HarnessModels(ctx context.Context, agent string) ([]string, error) {
	catalog, err := DiscoverHarnessModels(ctx, agent, false)
	return catalog.Models, err
}
