package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"central-memory/internal/continuex"
	"central-memory/internal/scan"
	"central-memory/internal/usage"
)

// ContinuationExchange is a durable in-app turn pair attached to its captured
// Nexus session. The original agent's local transcript is never modified.
type ContinuationExchange struct {
	ID          string                   `json:"id"`
	SessionID   string                   `json:"session_id"`
	Agent       string                   `json:"agent"`
	NativeID    string                   `json:"native_id,omitempty"`
	Prompt      string                   `json:"prompt"`
	Answer      string                   `json:"answer"`
	FileChanges []string                 `json:"file_changes,omitempty"`
	FileDiffs   []continuex.FileDiff     `json:"file_diffs,omitempty"`
	Activities  []continuex.ToolActivity `json:"activities,omitempty"`
	At          time.Time                `json:"at"`
	UserIdx     int64                    `json:"user_idx"`
	AnswerIdx   int64                    `json:"answer_idx"`
	CloudSaved  bool                     `json:"cloud_saved"`
	// Usage is the turn's token usage per model, when the agent reported it.
	Usage []usage.Entry `json:"usage,omitempty"`
}

func continuationRoot(override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return override, nil
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Nexus", "continuations"), nil
}

func continuationSessionDir(root, sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", errors.New("session id is required")
	}
	base, err := continuationRoot(root)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(sessionID))
	return filepath.Join(base, hex.EncodeToString(sum[:])), nil
}

func saveContinuation(root string, exchange ContinuationExchange) (ContinuationExchange, error) {
	dir, err := continuationSessionDir(root, exchange.SessionID)
	if err != nil {
		return exchange, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return exchange, err
	}
	if exchange.At.IsZero() {
		exchange.At = time.Now().UTC()
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return exchange, err
	}
	exchange.ID = exchange.At.Format("20060102T150405.000000000") + "-" + hex.EncodeToString(nonce[:])
	data, err := json.Marshal(exchange)
	if err != nil {
		return exchange, err
	}
	tmp, err := os.CreateTemp(dir, ".pending-*")
	if err != nil {
		return exchange, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return exchange, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return exchange, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return exchange, err
	}
	if err := tmp.Close(); err != nil {
		return exchange, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, exchange.ID+".json")); err != nil {
		return exchange, err
	}
	bumpDrivenIndex()
	return exchange, nil
}

func loadContinuations(root, sessionID string) ([]ContinuationExchange, error) {
	dir, err := continuationSessionDir(root, sessionID)
	if err != nil {
		return nil, err
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	items := make([]ContinuationExchange, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		var item ContinuationExchange
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		if item.SessionID != sessionID {
			return nil, errors.New("continuation session mismatch")
		}
		_, markerErr := os.Stat(filepath.Join(dir, item.ID+".synced"))
		item.CloudSaved = markerErr == nil
		items = append(items, item)
	}
	return items, nil
}

func markContinuationSynced(root, sessionID, exchangeID string) error {
	dir, err := continuationSessionDir(root, sessionID)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, exchangeID+".synced"), []byte{}, 0600)
}

// recordExchange saves one finished in-app turn locally and, when signed in,
// appends it to the cloud session so Timeline and teammates see it. Used by
// continue.here (one-shot turns) and continue.record (live sessions).
func recordExchange(ctx context.Context, deps Deps, sessionID, projectID, agent, nativeID, prompt, answer string, changedFiles []string, fileDiffs []continuex.FileDiff, activities []continuex.ToolActivity, seeded bool) map[string]any {
	return recordExchangeUsage(ctx, deps, sessionID, projectID, agent, nativeID, prompt, answer, changedFiles, fileDiffs, activities, seeded, nil)
}

// recordExchangeUsage is recordExchange plus the turn's token usage (live
// sessions report it). Usage is saved with the local exchange and sent to
// the cloud usage ledger.
func recordExchangeUsage(ctx context.Context, deps Deps, sessionID, projectID, agent, nativeID, prompt, answer string, changedFiles []string, fileDiffs []continuex.FileDiff, activities []continuex.ToolActivity, seeded bool, used []usage.Entry) map[string]any {
	used = validUsage(used)
	baseIdx := time.Now().UTC().UnixNano() / 10
	exchange, saveErr := saveContinuation(deps.ContinuationDir, ContinuationExchange{
		SessionID: sessionID, Agent: agent, NativeID: nativeID, Prompt: prompt, Answer: answer, FileChanges: changedFiles,
		Activities: activities,
		FileDiffs:  fileDiffs,
		UserIdx:    baseIdx, AnswerIdx: baseIdx + 1,
		Usage: used,
	})
	result := map[string]any{"text": answer, "agent": agent, "native_id": nativeID, "seeded": seeded,
		"file_changes":  changedFiles,
		"activities":    activities,
		"file_diffs":    fileDiffs,
		"saved_locally": saveErr == nil, "exchange_id": exchange.ID,
		"user_idx": exchange.UserIdx, "answer_idx": exchange.AnswerIdx}
	result["user_cursor"] = strconv.FormatInt(exchange.UserIdx, 10)
	result["answer_cursor"] = strconv.FormatInt(exchange.AnswerIdx, 10)
	if saveErr != nil {
		result["save_error"] = saveErr.Error()
	}
	if len(used) > 0 {
		result["usage"] = used
	}
	if saveErr == nil && deps.Cloud != nil && deps.Online && strings.TrimSpace(sessionID) != "" {
		toolCalls := make([]map[string]any, 0, len(changedFiles))
		for _, path := range changedFiles {
			changeArgs := map[string]string{"path": path}
			for _, change := range fileDiffs {
				if change.Path == path {
					changeArgs["diff"], changeArgs["notice"], changeArgs["status"] = change.Diff, change.Notice, change.Status
					break
				}
			}
			toolCalls = append(toolCalls, map[string]any{"name": "workspace_change", "args": changeArgs})
		}
		batch, _ := json.Marshal(map[string]any{"turns": []map[string]any{
			{"idx": baseIdx, "role": "user", "text_preview": prompt, "tool_calls": []any{}, "source_ts": exchange.At},
			{"idx": baseIdx + 1, "role": "assistant", "text_preview": answer, "tool_calls": toolCalls, "source_ts": exchange.At},
		}})
		if _, err := deps.Cloud.Do(ctx, http.MethodPost, "/v1/agent-sessions/"+url.PathEscape(sessionID)+"/turns/batch", batch); err == nil {
			result["cloud_saved"] = true
			_ = markContinuationSynced(deps.ContinuationDir, sessionID, exchange.ID)
			if len(used) > 0 {
				result["usage_saved"] = postExchangeUsage(ctx, deps, sessionID, exchange, used)
			}
			// Chats held in Nexus feed the memory layer like captured ones.
			result["memory_queued"] = queueMemoryExtract(ctx, deps, projectID, sessionID, prompt, answer)
		} else {
			result["cloud_saved"] = false
			result["sync_error"] = err.Error()
		}
	}
	return result
}

// queueMemoryExtract sends one finished exchange to the server's extraction
// queue (the same endpoint the capture pipeline uses), so decisions and facts
// from chats held in Nexus reach the Memory page. Best effort.
func queueMemoryExtract(ctx context.Context, deps Deps, projectID, sessionID, prompt, answer string) bool {
	projectID = strings.TrimSpace(projectID)
	if deps.Cloud == nil || projectID == "" || strings.TrimSpace(prompt+answer) == "" {
		return false
	}
	// Chat text goes to an extraction model: redact secrets first, exactly
	// as the capture path does for agent transcripts.
	clip := func(s string) string {
		s, _ = scan.RedactSecrets(s)
		s = strings.ToValidUTF8(strings.TrimSpace(s), "")
		if len(s) > 4000 {
			s = s[:4000]
			for len(s) > 0 && !utf8.ValidString(s) {
				s = s[:len(s)-1]
			}
		}
		return s
	}
	now := time.Now().UTC().Format(time.RFC3339)
	turns := []map[string]string{}
	if p := clip(prompt); p != "" {
		turns = append(turns, map[string]string{"speaker": "user", "content": p, "session_id": sessionID, "timestamp": now})
	}
	if a := clip(answer); a != "" {
		turns = append(turns, map[string]string{"speaker": "assistant", "content": a, "session_id": sessionID, "timestamp": now})
	}
	body, err := json.Marshal(map[string]any{"project_id": projectID, "turns": turns, "source": "desktop:chat"})
	if err != nil {
		return false
	}
	_, err = deps.Cloud.Do(ctx, http.MethodPost, "/memory/extract", body)
	return err == nil
}

// validUsage drops empty or out-of-range entries from an agent report.
func validUsage(in []usage.Entry) []usage.Entry {
	var out []usage.Entry
	for _, e := range in {
		if !e.Empty() && e.Valid() {
			out = append(out, e)
		}
	}
	return out
}

// postExchangeUsage sends a live turn's usage to the cloud usage ledger.
// It uses its own route rather than a field on the turn: servers decode
// turns strictly, so an older server would refuse the whole turn. Keys are
// per exchange and model, so a retry cannot double count.
func postExchangeUsage(ctx context.Context, deps Deps, sessionID string, exchange ContinuationExchange, used []usage.Entry) bool {
	records := make([]map[string]any, 0, len(used))
	for _, e := range used {
		record := map[string]any{
			"key": "live:" + exchange.ID + ":" + e.Model, "idx": exchange.AnswerIdx, "source_ts": exchange.At,
			"model": e.Model, "input": e.Input, "output": e.Output, "cache_read": e.CacheRead, "cache_write": e.CacheWrite,
		}
		if e.CostUSD != nil {
			record["cost_usd"] = *e.CostUSD
		}
		records = append(records, record)
	}
	body, err := json.Marshal(map[string]any{"records": records})
	if err != nil {
		return false
	}
	_, err = deps.Cloud.Do(ctx, http.MethodPost, "/v1/agent-sessions/"+url.PathEscape(sessionID)+"/usage", body)
	return err == nil
}
