package continuex

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenCodeToolDetailsShowCommandOutputErrorsAndTiming(t *testing.T) {
	var part openCodePart
	err := json.Unmarshal([]byte(`{"id":"p1","messageID":"m1","type":"tool","tool":"bash","state":{"status":"completed","title":"Run tests","input":{"command":"go test ./...","description":"Check all packages"},"output":"ok package/one\nok package/two\n","metadata":{"exit":0},"time":{"start":1000,"end":2250}}}`), &part)
	if err != nil {
		t.Fatal(err)
	}
	var shown []openCodeActivity
	tracker := newOpenCodeTextTracker("ses_test", func(p StreamProgress) {
		if p.Kind != "activity" {
			return
		}
		var activity openCodeActivity
		if err := json.Unmarshal([]byte(p.Text), &activity); err != nil {
			t.Fatal(err)
		}
		shown = append(shown, activity)
	})
	tracker.roles["m1"] = "assistant"
	tracker.observeActivity(part)
	if len(shown) != 1 || shown[0].Command != "go test ./..." || shown[0].Title != "Check all packages" ||
		!strings.Contains(shown[0].Output, "one\nok") || shown[0].ExitCode == nil || *shown[0].ExitCode != 0 || shown[0].DurationMS != 1250 {
		t.Fatalf("missing details: %+v", shown)
	}
	part.State.Output = "new output"
	tracker.observeActivity(part)
	tracker.observeActivity(part)
	if len(shown) != 2 || shown[1].Output != "new output" {
		t.Fatalf("same-status output update lost: %+v", shown)
	}
	part.State.Status, part.State.Error = "error", "test failed"
	tracker.observeActivity(part)
	if len(shown) != 3 || shown[2].Error != "test failed" {
		t.Fatalf("error detail lost: %+v", shown)
	}
}

func TestOpenCodeToolDetailsRedactCredentialsAndSensitiveFiles(t *testing.T) {
	var part openCodePart
	err := json.Unmarshal([]byte(`{"type":"tool","state":{"input":{"command":"curl --token hide-flag https://example.test","headers":{"Authorization":"Bearer hide-header"},"config":{"api_key":"hide-nested"}},"output":"password=hide-password\nAuthorization: Bearer hide-bearer\nhttps://user:hide-url@example.test\n-----BEGIN PRIVATE KEY-----\nhide-pem\n-----END PRIVATE KEY-----"}}`), &part)
	if err != nil {
		t.Fatal(err)
	}
	activity := openCodeActivity{}
	populateOpenCodeToolDetails(&activity, part)
	encoded, _ := json.Marshal(activity)
	for _, secret := range []string{"hide-flag", "hide-header", "hide-nested", "hide-password", "hide-bearer", "hide-url", "hide-pem"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("credential leaked: %s", secret)
		}
	}
	part.State.Input = json.RawMessage(`{"filePath":"C:\\work\\.env","content":"sensitive-file-content"}`)
	part.State.Output = "sensitive-file-content"
	populateOpenCodeToolDetails(&activity, part)
	if strings.Contains(activity.Input+activity.Output, "sensitive-file-content") {
		t.Fatal("sensitive file contents exposed")
	}
}

func TestOpenCodeToolDetailsBoundPreviewsPreserveLinesAndStripTerminalCodes(t *testing.T) {
	text := activityDetail("\x1b[31mfirst\n\tsecond\x1b[0m\x00", 100)
	if text != "first\n\tsecond" {
		t.Fatalf("unreadable output: %q", text)
	}
	if text := activityDetail(strings.Repeat("界", 30), 10); !strings.HasPrefix(text, strings.Repeat("界", 10)) || !strings.Contains(text, "truncated") {
		t.Fatalf("bad Unicode preview: %q", text)
	}
}
