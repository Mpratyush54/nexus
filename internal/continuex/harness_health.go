package continuex

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// One provider-failure policy for every hosted harness. Only call it with a
// diagnostic/error envelope, never ordinary assistant prose or tool results.
type harnessFailure struct {
	agent string
	kind  string
	wait  time.Duration
}

func harnessName(agent string) string {
	switch normalizeAgent(agent) {
	case "opencode":
		return "OpenCode"
	case "cursor", "agent":
		return "Cursor"
	case "codex":
		return "Codex"
	case "claude":
		return "Claude Code"
	case "gemini":
		return "Gemini"
	case "kimi":
		return "Kimi"
	default:
		return progressLabel(agent, 40)
	}
}

func (e *harnessFailure) Error() string {
	prefix := harnessName(e.agent)
	var message string
	switch e.kind {
	case "quota":
		message = "free usage, quota, or billing limit reached. Choose another available model/provider or check the account plan and quota reset before retrying."
	case "auth":
		message = "provider authentication failed. Sign in or reconnect the provider in the native agent, then retry."
	case "rate_limit":
		message = "provider rate limit reached. Wait for the limit to reset or choose another available model/provider, then retry explicitly."
	case "model":
		message = "selected or saved model is unavailable or no longer supported. Select an available model/provider before retrying."
	case "context":
		message = "model context limit was exceeded. Fork with a shorter summary or choose a larger context window."
	case "unavailable":
		message = "provider is unavailable or overloaded. Wait or choose another available model/provider, then retry explicitly."
	case "retry":
		message = "automatic retry exceeded the in-app limit. Nexus stopped this request; check the provider or choose another model before retrying."
	default:
		message = "reported an execution failure. Review the native agent session before retrying; any workspace changes already made are not undone."
	}
	if e.wait > 0 {
		message += " The provider scheduled its next retry in " + e.wait.Round(time.Second).String() + "."
	}
	return prefix + " " + message
}

var diagnosticCode = regexp.MustCompile(`\b(?:401|402|403|429|503)\b`)
var diagnosticRetryWait = regexp.MustCompile(`(?i)\bretrying (?:in|after)\s+((?:\d+(?:\.\d+)?\s*(?:ms|s|m|h)\s*)+)`)
var diagnosticRetryAttempt = regexp.MustCompile(`(?i)\battempt\s*#?\s*(\d+)`)

func providerDiagnostic(agent, diagnostic string) *harnessFailure {
	s := strings.ToLower(diagnostic)
	contains := func(needles ...string) bool {
		for _, needle := range needles {
			if strings.Contains(s, needle) {
				return true
			}
		}
		return false
	}
	code := diagnosticCode.FindString(s)
	kind := ""
	switch {
	case contains("free usage exceeded", "free usage limit", "usage limit", "quota", "resource_exhausted", "credit balance", "insufficient credit", "insufficient balance", "insufficient_balance", "not enough credits", "daily limit", "monthly limit", "spending limit", "billing", "payment required", "payment_required", "usage_limit_reached") || code == "402":
		kind = "quota"
	case contains("authentication", "unauthenticated", "unauthorized", "invalid_api_key", "invalid api key", "invalid_api_token", "no api key", "missing api key", "api_key not set", "api_key is not set", "not logged in", "not signed in", "sign in required", "login required", "login_required", "please run /login", "providerautherror", "auth_error") || code == "401" || code == "403":
		kind = "auth"
	case contains("context_length", "context window", "context limit", "token limit", "too many tokens"):
		kind = "context"
	case contains("rate limit", "rate_limit", "too many requests") || code == "429":
		kind = "rate_limit"
	case contains("modelnotfound", "model_not_found", "model not found", "model is unavailable", "model unavailable", "unsupported model", "model is not supported", "model does not exist", "model has been retired", "model was retired", "providermodelnotfounderror") || (contains("model") && contains("not found", "does not exist", "unavailable", "retired", "not supported")):
		kind = "model"
	case contains("overloaded", "over_capacity", "service unavailable", "server unavailable") || code == "503":
		kind = "unavailable"
	}
	if kind == "" {
		return nil
	}
	return &harnessFailure{agent: agent, kind: kind}
}

const maxProviderRetries = 2
const maxProviderRetryWait = 30 * time.Second

// A retry status is not a normal busy status. Hard failures stop immediately;
// transient retries get at most two attempts with a <=30-second delay each.
func retryDiagnostic(agent, message string, attempt int, wait time.Duration) (string, error) {
	if problem := providerDiagnostic(agent, message); problem != nil {
		problem.wait = wait
		return "", problem
	}
	if attempt > maxProviderRetries || wait > maxProviderRetryWait {
		return "", &harnessFailure{agent: agent, kind: "retry", wait: wait}
	}
	if attempt <= 0 {
		attempt = 1
	}
	status := fmt.Sprintf("%s provider retry %d/%d", harnessName(agent), attempt, maxProviderRetries)
	if wait > 0 {
		status += " · next attempt in " + wait.Round(time.Second).String()
	}
	return status, nil
}

// Restrict inspection to documented failure-like envelopes. A successful
// answer about quotas, or a failed workspace tool, must not stop the agent.
func streamHarnessProblem(agent, line string, stderr bool) error {
	line = strings.TrimSpace(activityANSI.ReplaceAllString(line, ""))
	if line == "" {
		return nil
	}
	if _, err, found := streamRetryHealth(agent, line, stderr); found {
		return err
	}
	var row map[string]any
	if json.Unmarshal([]byte(line), &row) != nil {
		if strings.HasPrefix(line, "{") || strings.HasPrefix(line, "[") {
			return nil
		}
		if isPlainDiagnostic(line) || (stderr && isProviderDiagnosticLine(line)) {
			if problem := providerDiagnostic(agent, line); problem != nil {
				return problem
			}
		}
		return nil
	}
	str := func(key string) string { s, _ := row[key].(string); return s }
	failure := false
	switch str("type") {
	case "error", "turn.failed", "session.error", "session.execution.failed", "response.failed":
		failure = true
	case "result":
		isError, _ := row["is_error"].(bool)
		failure = isError || str("status") == "error" || strings.HasPrefix(str("subtype"), "error")
	case "assistant":
		failure = diagnosticPresent(row["error"])
	case "system":
		failure = str("subtype") == "error"
	case "rate_limit_event":
		info, _ := row["rate_limit_info"].(map[string]any)
		if info["status"] == "rejected" {
			return &harnessFailure{agent: agent, kind: "rate_limit"}
		}
	default:
		failure = stderr && (str("level") == "error" || str("level") == "fatal")
	}
	if !failure {
		return nil
	}
	if problem := providerDiagnostic(agent, line); problem != nil {
		return problem
	}
	return &harnessFailure{agent: agent, kind: "runtime"}
}

func isProviderDiagnosticLine(line string) bool {
	s := strings.ToLower(line)
	for _, phrase := range []string{"free usage exceeded", "quota exceeded", "exceeded your current quota", "usage limit reached", "hit your usage limit", "credit balance", "insufficient credit", "resource_exhausted", "authentication failed", "invalid api key", "no api key", "missing api key", "api_key not set", "api_key is not set", "please run /login", "not logged in", "not signed in", "rate limit", "rate_limit_error", "too many requests", "model not found", "context window exceeded", "service unavailable", "provider overloaded"} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

func streamRetryHealth(agent, line string, stderr bool) (string, error, bool) {
	var row map[string]any
	if json.Unmarshal([]byte(line), &row) == nil {
		kind, _ := row["type"].(string)
		subtype, _ := row["subtype"].(string)
		apiRetry := kind == "system" && subtype == "api_retry"
		if kind != "retry" && kind != "retrying" && !apiRetry {
			return "", nil, false
		}
		message, _ := row["message"].(string)
		attempt, _ := row["attempt"].(float64)
		wait, _ := row["retry_after"].(float64)
		if apiRetry {
			ms, _ := row["retry_delay_ms"].(float64)
			wait = ms / 1000
			message = line
		}
		status, err := retryDiagnostic(agent, message, int(attempt), time.Duration(wait*float64(time.Second)))
		return status, err, true
	}
	if !stderr && !isPlainDiagnostic(line) {
		return "", nil, false
	}
	match := diagnosticRetryWait.FindStringSubmatch(line)
	if len(match) < 2 {
		return "", nil, false
	}
	wait, err := time.ParseDuration(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(match[1])), " ", ""))
	if err != nil {
		return "", nil, false
	}
	attempt := 1
	if match := diagnosticRetryAttempt.FindStringSubmatch(line); len(match) > 1 {
		attempt, _ = strconv.Atoi(match[1])
	}
	status, problem := retryDiagnostic(agent, line, attempt, wait)
	return status, problem, true
}

func diagnosticPresent(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return strings.TrimSpace(v) != ""
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	default:
		return true
	}
}

func isPlainDiagnostic(line string) bool {
	s := strings.ToLower(strings.TrimSpace(line))
	for _, prefix := range []string{"error:", "error ", "free usage exceeded", "quota exceeded", "rate limit exceeded", "authentication failed", "failed to authenticate", "you have hit your usage limit", "you've hit your usage limit"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
