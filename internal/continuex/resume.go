// Package continuex builds the Continue command (D2).
package continuex

import (
	"errors"
	"strings"
)

// Request is continue.start.
type Request struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"`   // here | open_in_agent
	Resume    string `json:"resume"` // native | fork | seeded
	Agent     string `json:"agent"`
	Prompt    string `json:"prompt"`
}

// Command returns the argv the core runs for this continue.
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
