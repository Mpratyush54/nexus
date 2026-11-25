package continuex

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"central-memory/internal/secrets"
)

var (
	activityANSI       = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)
	activityPEM        = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----.*?(?:-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----|$)`)
	activityCredential = regexp.MustCompile(`(?i)(["']?(?:[\w.-]*(?:password|passwd|api[_-]?key|access[_-]?token|refresh[_-]?token|secret|authorization|cookie|credential)|token)["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)
	activitySecretFlag = regexp.MustCompile(`(?i)(--(?:password|token|api-key|api_key|secret|access-token|credential)\s+)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)
	activityAuth       = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
	activityURLAuth    = regexp.MustCompile(`(https?://)[^\s/@:]+:[^\s/@]+@`)
	activityKnownToken = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,})\b`)
)

// Redact before truncating so a boundary cannot expose half a credential.
// Preserve newlines, indentation and Unicode in commands and tool output.
func activityDetail(s string, limit int) string {
	s = activityANSI.ReplaceAllString(s, "")
	s = activityPEM.ReplaceAllString(s, "[redacted private key]")
	s = activityAuth.ReplaceAllString(s, "${1} [redacted]")
	s = activityCredential.ReplaceAllString(s, "${1}[redacted]")
	s = activitySecretFlag.ReplaceAllString(s, "${1}[redacted]")
	s = activityURLAuth.ReplaceAllString(s, "${1}[redacted]@")
	s = activityKnownToken.ReplaceAllString(s, "[redacted]")
	s = secrets.RedactText(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	runes := []rune(strings.TrimSpace(s))
	if len(runes) > limit {
		return string(runes[:limit]) + "\n… Preview truncated; open the native session for the full result."
	}
	return string(runes)
}

func sensitiveActivityKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
	return strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "apikey") ||
		strings.Contains(key, "credential") || strings.Contains(key, "authorization") || strings.Contains(key, "cookie") ||
		key == "token" || strings.HasSuffix(key, "token")
}

func redactActivityInput(value any, depth int) any {
	if depth > 12 {
		return "[nested details omitted]"
	}
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if sensitiveActivityKey(key) {
				v[key] = "[redacted]"
			} else {
				v[key] = redactActivityInput(child, depth+1)
			}
		}
	case []any:
		for i, child := range v {
			v[i] = redactActivityInput(child, depth+1)
		}
	case string:
		return activityDetail(v, 8000)
	}
	return value
}

func populateOpenCodeToolDetails(activity *openCodeActivity, part openCodePart) {
	var input any
	_ = json.Unmarshal(part.State.Input, &input)
	secretFile := false
	if object, ok := input.(map[string]any); ok {
		text := func(key string) string { s, _ := object[key].(string); return s }
		activity.Command = activityDetail(text("command"), 4000)
		activity.Title = activityDetail(text("description"), 180)
		for _, key := range []string{"filePath", "file_path", "path"} {
			if path := progressLabel(text(key), 512); path != "" {
				activity.Files = append(activity.Files, path)
				secretFile = secretFile || secrets.DetectSecretPath(path)
			}
		}
	}
	if input != nil {
		if encoded, err := json.MarshalIndent(redactActivityInput(input, 0), "", "  "); err == nil {
			activity.Input = activityDetail(string(encoded), 8000)
		}
	}
	if activity.Title == "" {
		activity.Title = activityDetail(part.State.Title, 180)
	}
	output := part.State.Output
	if output == "" {
		output = part.State.Metadata.Output
	}
	if part.State.Metadata.Diff != "" {
		output += "\n\nDiff\n" + part.State.Metadata.Diff
	}
	activity.Output = activityDetail(output, 16000)
	activity.Error = activityDetail(part.State.Error, 2000)
	if secretFile {
		activity.Output = "Output hidden because this tool accessed a sensitive file. Review it in the native session."
		// An edit/write input may contain that file's entire contents.
		activity.Input = "Input details hidden because this tool accessed a sensitive file."
	}
	activity.ExitCode = part.State.Metadata.Exit
	if activity.ExitCode == nil {
		activity.ExitCode = part.State.Metadata.ExitCode
	}
	if part.State.Time.Start > 0 && part.State.Time.End >= part.State.Time.Start {
		activity.DurationMS = part.State.Time.End - part.State.Time.Start
	}
}
