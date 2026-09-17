// Tests for the sync secret-gate patterns (issue #47).
//
// NeverPatterns fails a sync closed on hit, so the table pins both sides:
// real secret shapes must match, and everyday content (docs mentioning key
// names, short/placeholder values, normal file paths) must NOT match — a
// false positive here blocks every sync.
package scan

import "testing"

func TestNeverPatternsSecretsMatch(t *testing.T) {
	secrets := []struct {
		name  string
		input string
	}{
		{"gitlab token", "deploys with glpat-AbC123_x-9z inside"},
		{"github token", "token=ghp_AbC123xYz987qWeRtYUiOp"},
		{"anthropic key", "key: sk-ant-AbC123-xyZ987-long-value"},
		{"aws access key", "AKIAIOSFODNN7EXAMPLE in env"},
		{"api_key equals long value", `api_key="AbC123xYz987qWeRtYUiOp9"`},
		{"api-key colon long value", "api-key: AbC123xYz987qWeRtYUiOp9"},
		{"apikey spaced long value", "APIKEY = 'AbC123xYz987qWeRtYUiOp9'"},
	}
	for _, tc := range secrets {
		t.Run(tc.name, func(t *testing.T) {
			matched := false
			for _, re := range NeverPatterns {
				if re.MatchString(tc.input) {
					matched = true
					break
				}
			}
			if !matched {
				t.Errorf("secret %q matched no NeverPattern", tc.input)
			}
		})
	}
}

func TestNeverPatternsNormalPathsDoNotMatch(t *testing.T) {
	clean := []struct {
		name  string
		input string
	}{
		{"go file", "package main\nfunc main() {}\n"},
		{"relative path", "src/server/routes.go"},
		{"docs mention without value", "set api_key in your environment before syncing"},
		{"docs mention with short value", `api_key="test"`},
		{"short equals value", "api-key=changeme"},
		{"lowercase akia-like", "akia is not a real key prefix here"},
		{"github placeholder", "ghp_ is issued per user, see docs"},
		{"pem filename only", "certs/server.pem"},
		{"env filename only", ".env.example"},
		{"empty", ""},
	}
	for _, tc := range clean {
		t.Run(tc.name, func(t *testing.T) {
			for _, re := range NeverPatterns {
				if re.MatchString(tc.input) {
					t.Errorf("clean input %q matched %q", tc.input, re.String())
				}
			}
		})
	}
}
