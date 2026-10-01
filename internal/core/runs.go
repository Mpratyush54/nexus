package core

import (
	"errors"
	"strings"
	"sync"
)

// runRec is in-memory state for a Continue / Here-in-Nexus run (op_id).
// Enough for the UI to call runs.message / approve / cancel.
type runRec struct {
	Status   string   // starting | running | awaiting_approval | approved | cancelled
	Messages []string // user messages posted via runs.message
	Argv     []string
}

var (
	runMu sync.Mutex
	runs  = map[string]*runRec{}
)

func trackRun(opID string, argv []string) {
	opID = strings.TrimSpace(opID)
	if opID == "" {
		return
	}
	runMu.Lock()
	defer runMu.Unlock()
	runs[opID] = &runRec{Status: "running", Argv: append([]string(nil), argv...)}
}

func runMessage(opID, text string) (map[string]any, error) {
	opID = strings.TrimSpace(opID)
	if opID == "" {
		return nil, errors.New("runs.message: op_id is required")
	}
	runMu.Lock()
	defer runMu.Unlock()
	r, ok := runs[opID]
	if !ok {
		return nil, errors.New("runs.message: unknown op_id")
	}
	if r.Status == "cancelled" {
		return nil, errors.New("runs.message: run is cancelled")
	}
	text = strings.TrimSpace(text)
	if text != "" {
		r.Messages = append(r.Messages, text)
	}
	if r.Status == "running" {
		r.Status = "awaiting_approval"
	}
	return map[string]any{
		"op_id":    opID,
		"status":   r.Status,
		"messages": len(r.Messages),
	}, nil
}

func runApprove(opID string) (map[string]any, error) {
	opID = strings.TrimSpace(opID)
	if opID == "" {
		return nil, errors.New("runs.approve: op_id is required")
	}
	runMu.Lock()
	defer runMu.Unlock()
	r, ok := runs[opID]
	if !ok {
		return nil, errors.New("runs.approve: unknown op_id")
	}
	switch r.Status {
	case "cancelled":
		return nil, errors.New("runs.approve: run is cancelled")
	case "approved":
		return map[string]any{"op_id": opID, "status": r.Status}, nil
	}
	r.Status = "approved"
	return map[string]any{"op_id": opID, "status": r.Status}, nil
}

func runCancel(opID string) (map[string]any, error) {
	opID = strings.TrimSpace(opID)
	if opID == "" {
		return nil, errors.New("runs.cancel: op_id is required")
	}
	runMu.Lock()
	defer runMu.Unlock()
	r, ok := runs[opID]
	if !ok {
		return nil, errors.New("runs.cancel: unknown op_id")
	}
	r.Status = "cancelled"
	return map[string]any{"op_id": opID, "status": r.Status}, nil
}

// ResetRunsForTest clears in-memory run state (tests only).
func ResetRunsForTest() {
	runMu.Lock()
	defer runMu.Unlock()
	runs = map[string]*runRec{}
}
