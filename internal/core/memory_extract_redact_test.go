package core

import (
	"context"
	"strings"
	"testing"
)

type captureCloud struct{ body string }

func (c *captureCloud) Do(_ context.Context, _, _ string, body []byte) ([]byte, error) {
	c.body = string(body)
	return []byte(`{}`), nil
}

// In-app chat text sent for memory extraction must not carry secrets.
func TestMemoryExtractRedactsSecrets(t *testing.T) {
	cloud := &captureCloud{}
	key := "sk-" + strings.Repeat("a", 30)
	ok := queueMemoryExtract(context.Background(), Deps{Cloud: cloud}, "proj", "sess",
		"use the key "+key+" and token github_pat_"+strings.Repeat("b", 25), "done, used Bearer "+strings.Repeat("c", 30))
	if !ok {
		t.Fatal("extract not queued")
	}
	for _, leak := range []string{key, "github_pat_bbbb", strings.Repeat("c", 30)} {
		if strings.Contains(cloud.body, leak) {
			t.Fatalf("secret %q sent for extraction: %s", leak, cloud.body)
		}
	}
	if !strings.Contains(cloud.body, "[REDACTED]") {
		t.Fatalf("no redaction marker: %s", cloud.body)
	}
}
