package scan

import (
	"strings"
	"testing"
)

func auditMatched(s string) (bool, string) {
	for _, re := range NeverPatterns {
		if re.MatchString(s) {
			return true, re.String()
		}
	}
	return false, ""
}

func TestAuditNeverPatternsEachHits(t *testing.T) {
	// NOTE: fixtures assembled via concatenation so the file holds no literal
	// token-shaped strings (secret-scanning push protection).
	cases := []struct{ name, input string }{
		{"glpat", "deploy " + "glpat-" + "AbC123_x-9z here"},
		{"ghp", "tok " + "ghp_" + "AbC123xYz987qWeRtYUiOp here"},
		{"sk-ant", "key " + "sk-ant-" + "AbC123-xyZ987-long-value here"},
		{"akia", "AKIAIOSFODNN7" + "EXAMPLE here"},
		{"api_key long", `api_key="AbC123xYz987qWeRtYUiOp9"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ok, _ := auditMatched(tc.input); !ok {
				t.Errorf("input %q matched nothing, want a hit", tc.input)
			}
		})
	}
}

func TestAuditApiKeyLengthBounds(t *testing.T) {
	long20 := strings.Repeat("A", 20)
	long2000 := strings.Repeat("B", 2000)
	short19 := strings.Repeat("C", 19)
	if ok, _ := auditMatched(`api_key="` + long20 + `"`); !ok {
		t.Errorf("api_key with 20 chars should match")
	}
	if ok, _ := auditMatched(`api_key="` + long2000 + `"`); !ok {
		t.Errorf("api_key with 2000 chars should match (no upper bound)")
	}
	if ok, pat := auditMatched(`api_key="` + short19 + `"`); ok {
		t.Errorf("api_key with 19 chars should NOT match (matched %s)", pat)
	}
	if ok, pat := auditMatched(`api_key="test"`); ok {
		t.Errorf("placeholder api_key should not match (matched %s)", pat)
	}
	// Prose without a value must not block sync.
	if ok, pat := auditMatched("set api_key in your environment before syncing"); ok {
		t.Errorf("prose mention should not match (matched %s)", pat)
	}
}

func TestAuditApiKeySeparators(t *testing.T) {
	val := strings.Repeat("Z", 24)
	mustMatch := []string{
		"api_key=" + val,
		"api-key:" + val,
		`API_KEY = "` + val + `"`,
		"APIKEY=" + val,
		"api-key = '" + val + "'",
	}
	for _, in := range mustMatch {
		if ok, _ := auditMatched(in); !ok {
			t.Errorf("separator variant %q should match", in)
		}
	}
	// No [: =] separator -> must NOT match (avoids prose false-positives).
	mustNot := []string{
		"apikey " + val,
		"api key: " + val, // space inside key name is not a supported separator
	}
	for _, in := range mustNot {
		if ok, pat := auditMatched(in); ok {
			t.Errorf("variant %q should NOT match (matched %s)", in, pat)
		}
	}
}

// REGRESSION (actual behavior): NeverPatterns misses modern secret shapes, so
// real secrets pass the gate.
// BUG tracked in #118: patterns needed for OpenAI project/legacy keys, Slack
// tokens, private-key blocks, GitHub fine-grained tokens, AWS secret keys,
// and generic bearer tokens. This test pins the CURRENT misses (no match).
func TestAuditNeverPatternsBypassGapsKnownBug(t *testing.T) {
	// NOTE: fixtures are assembled via concatenation so the file contains no
	// literal token-shaped strings (secret-scanning push protection).
	bypasses := []struct{ name, input string }{
		{"openai project key", "key is " + "sk-" + "proj-AbC123xYz987qWeRtYUiOp9abc here"},
		{"openai legacy", "sk-" + "AbC123xYz987qWeRtYUiOp9abcDEF1234 here"},
		{"slack xox", "xox" + "b-123456789012-AbC123xYz987qWeRtYUiOp here"},
		{"private key block", "-----BEGIN PRIVATE KEY-----\nMIIEvgIBADANBgkqhkiG9w0\n-----END PRIVATE KEY-----"},
		{"rsa private key", "-----BEGIN RSA PRIVATE KEY-----"},
		{"github fine-grained", "github_" + "pat_AbC123xYz987qWeRtYUiOp9abcDEF1234 here"},
		{"aws secret key", `aws_secret_access_key = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"`},
		{"generic bearer", "Authorization: Bearer " + "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig"},
	}
	for _, tc := range bypasses {
		t.Run(tc.name, func(t *testing.T) {
			if ok, pat := auditMatched(tc.input); ok {
				t.Errorf("expected pinned miss for %s, but matched %s (BUG #118: gate should fail closed)", tc.name, pat)
			}
		})
	}
}

func TestAuditShouldSkipWholeSegments(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{`vault/raw/agents/x.json`, true},
		{"vault/raw/agents/x.json", true},
		{`vault/drive-mirror/f.txt`, true},
		{`vault/draw/plan.md`, false}, // "draw" must not match "raw"
		{`vault/brawny/f.txt`, false},
		{`vault/rawness/f.txt`, false},
		{"", false},
		{`RAW/agents/x`, false}, // case-sensitive segment match
	}
	for _, tc := range cases {
		if got := ShouldSkip(tc.path); got != tc.want {
			t.Errorf("ShouldSkip(%q) = %v want %v", tc.path, got, tc.want)
		}
	}
}

func TestAuditSkipDirsAndDriveIgnore(t *testing.T) {
	if len(SkipDirs) != 2 {
		t.Errorf("SkipDirs len = %d want 2", len(SkipDirs))
	}
	found := map[string]bool{}
	for _, d := range SkipDirs {
		found[d] = true
	}
	if !found["raw"] || !found["drive-mirror"] {
		t.Errorf("SkipDirs = %v want raw+drive-mirror", SkipDirs)
	}
	if len(DriveIgnore) == 0 {
		t.Fatal("DriveIgnore empty")
	}
	need := []string{"node_modules/", ".git/", "*.log", ".env*"}
	for _, n := range need {
		ok := false
		for _, d := range DriveIgnore {
			if d == n {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("DriveIgnore missing %q (got %v)", n, DriveIgnore)
		}
	}
}

func TestAuditCadenceMentionsTiers(t *testing.T) {
	if Cadence == "" {
		t.Fatal("Cadence empty")
	}
	for _, want := range []string{"restic", "azure", "15min"} {
		if !strings.Contains(strings.ToLower(Cadence), want) {
			t.Errorf("Cadence should mention %q: %q", want, Cadence)
		}
	}
}
