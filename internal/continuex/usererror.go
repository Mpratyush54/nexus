package continuex

import (
	"errors"
	"fmt"
	"strings"
)

// DetailMarker separates the sentence people see from the technical detail
// that only belongs in the log. The desktop shows the text before it, logs
// the whole error under a reference id, and offers the detail only inside
// an "Advanced details" expander.
const DetailMarker = " [detail: "

// AgentError is a failure to start or talk to a coding agent, worded for
// people. Detail keeps the technical cause for the log.
type AgentError struct {
	Agent  string
	Kind   string // not_installed, cannot_start, signed_out, failed
	Detail string
}

func (e *AgentError) Message() string {
	name := AgentDisplayName(e.Agent)
	switch e.Kind {
	case "not_installed":
		return name + " isn't installed on this computer."
	case "signed_out":
		return name + " isn't signed in on this computer."
	case "failed":
		return name + " stopped before it finished replying. Try sending again."
	case "timeout":
		return name + " took too long to reply, so Nexus stopped it. Try again, or ask for something smaller."
	case "empty":
		return name + " finished without a reply Nexus could show. Try sending again."
	default:
		return "Nexus couldn't start " + name + " on this computer."
	}
}

func (e *AgentError) Error() string {
	if strings.TrimSpace(e.Detail) == "" {
		return e.Message()
	}
	return e.Message() + DetailMarker + strings.TrimSpace(e.Detail) + "]"
}

// launcherError: the agent's launcher was found, but no program behind it.
type launcherError struct{ agent, path string }

func (e *launcherError) Error() string { return e.agentError().Error() }

func (e *launcherError) agentError() *AgentError {
	return &AgentError{Agent: e.agent, Kind: "cannot_start", Detail: "launcher " + e.path + " has no runnable program behind it"}
}

// AsAgentError wraps any error from starting an agent into an AgentError,
// keeping one that already is.
func AsAgentError(agent, kind string, err error) error {
	if err == nil {
		return nil
	}
	var ae *AgentError
	if errors.As(err, &ae) {
		return ae
	}
	var le *launcherError
	if errors.As(err, &le) {
		return le.agentError()
	}
	var hf *harnessFailure
	if errors.As(err, &hf) || isPlainSentence(err.Error()) {
		return err // already written for people
	}
	return &AgentError{Agent: normalizeAgent(agent), Kind: kind, Detail: err.Error()}
}

// AgentDisplayName is the product name people know an agent by.
func AgentDisplayName(agent string) string {
	switch normalizeAgent(agent) {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "cursor", "agent":
		return "Cursor"
	case "opencode":
		return "OpenCode"
	case "gemini":
		return "Gemini"
	case "kimi":
		return "Kimi Code"
	case "copilot":
		return "GitHub Copilot"
	case "agy":
		return "Antigravity"
	case "":
		return "the agent"
	default:
		a := normalizeAgent(agent)
		return strings.ToUpper(a[:1]) + a[1:]
	}
}

func agentErrorf(agent, kind, format string, args ...any) error {
	return &AgentError{Agent: normalizeAgent(agent), Kind: kind, Detail: fmt.Sprintf(format, args...)}
}

// isPlainSentence: Nexus writes messages meant for people as sentences that
// start with a capital letter; Go and agent errors are lowercase or carry
// "pkg: detail" prefixes.
func isPlainSentence(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, DetailMarker) {
		return s != ""
	}
	first := s[0]
	if first < 'A' || first > 'Z' {
		return false
	}
	head, _, _ := strings.Cut(s, " ")
	return !strings.Contains(s, ": ") && !strings.HasSuffix(head, ":")
}
