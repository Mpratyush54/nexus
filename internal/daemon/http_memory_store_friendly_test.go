package daemon

import (
	"strings"
	"testing"
)

func TestExtractRemoteFriendlyErrorsNoHTTP(t *testing.T) {
	s := NewHTTPMemoryStore("http://127.0.0.1:1", "", "")
	_, _, err := s.ExtractRemote(t.Context(), []map[string]string{{"content": "hello world this is long enough"}})
	if err == nil || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("empty token: %v", err)
	}

	s = NewHTTPMemoryStore("http://127.0.0.1:1", "tok", "")
	_, _, err = s.ExtractRemote(t.Context(), []map[string]string{{"content": "hello world this is long enough"}})
	if err == nil || !strings.Contains(err.Error(), "no workspace folder linked") {
		t.Fatalf("empty project: %v", err)
	}
}
