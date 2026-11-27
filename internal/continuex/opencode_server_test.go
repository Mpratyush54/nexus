package continuex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenCodeTextTrackerFiltersOtherSessionsAndNonAssistantParts(t *testing.T) {
	var seen []string
	tracker := newOpenCodeTextTracker("ses_ours", func(p StreamProgress) {
		if p.Kind == "text" {
			seen = append(seen, p.Text)
		}
	})
	rows := []string{
		`{"type":"message.updated","properties":{"info":{"id":"msg_other","sessionID":"ses_other","role":"assistant"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"part_other","sessionID":"ses_other","messageID":"msg_other","type":"text","text":"PRIVATE"}}}`,
		`{"type":"message.updated","properties":{"info":{"id":"msg_user","sessionID":"ses_ours","role":"user"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"part_user","sessionID":"ses_ours","messageID":"msg_user","type":"text","text":"secret prompt"}}}`,
		`{"type":"message.updated","properties":{"info":{"id":"msg_assistant","sessionID":"ses_ours","role":"assistant"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"part_tool","sessionID":"ses_ours","messageID":"msg_assistant","type":"tool","text":"secret tool arguments"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"part_text","sessionID":"ses_ours","messageID":"msg_assistant","type":"text","text":""}}}`,
		`{"type":"message.part.delta","properties":{"sessionID":"ses_ours","messageID":"msg_assistant","partID":"part_text","field":"text","delta":"Hello"}}`,
		`{"type":"message.part.delta","properties":{"sessionID":"ses_other","messageID":"msg_assistant","partID":"part_text","field":"text","delta":"PRIVATE"}}`,
	}
	for _, row := range rows {
		var event openCodeEvent
		if err := json.Unmarshal([]byte(row), &event); err != nil {
			t.Fatal(err)
		}
		tracker.accept(event)
	}
	if got := tracker.text(); got != "Hello" || len(seen) != 1 || seen[0] != "Hello" {
		t.Fatalf("filtered stream: text=%q updates=%q", got, seen)
	}
}

func TestOpenCodeTextTrackerStreamsV2AssistantDeltas(t *testing.T) {
	var seen []string
	tracker := newOpenCodeTextTracker("ses_ours", func(p StreamProgress) {
		if p.Kind == "text" {
			seen = append(seen, p.Text)
		}
	})
	rows := []string{
		`{"type":"session.next.reasoning.delta","properties":{"sessionID":"ses_ours","assistantMessageID":"msg_1","delta":"SECRET"}}`,
		`{"type":"session.next.tool.input.delta","properties":{"sessionID":"ses_ours","assistantMessageID":"msg_1","delta":"PRIVATE"}}`,
		`{"type":"session.next.text.delta","properties":{"sessionID":"ses_other","assistantMessageID":"msg_1","textID":"text_1","delta":"OTHER"}}`,
		`{"type":"session.next.text.started","properties":{"sessionID":"ses_ours","assistantMessageID":"msg_1","textID":"text_1"}}`,
		`{"type":"session.next.text.delta","properties":{"sessionID":"ses_ours","assistantMessageID":"msg_1","textID":"text_1","delta":"Hello"}}`,
		`{"type":"session.next.text.delta","properties":{"sessionID":"ses_ours","assistantMessageID":"msg_1","textID":"text_1","delta":" world"}}`,
		`{"type":"session.next.text.ended","properties":{"sessionID":"ses_ours","assistantMessageID":"msg_1","textID":"text_1","text":"Hello world"}}`,
	}
	for _, row := range rows {
		var event openCodeEvent
		if err := json.Unmarshal([]byte(row), &event); err != nil {
			t.Fatal(err)
		}
		tracker.accept(event)
	}
	if tracker.text() != "Hello world" || len(seen) == 0 || seen[0] != "Hello" || strings.Contains(strings.Join(seen, " "), "SECRET") {
		t.Fatalf("v2 stream: text=%q updates=%q", tracker.text(), seen)
	}
}

func TestOpenCodeHTTPStreamsBeforeCompletion(t *testing.T) {
	events := make(chan string, 16)
	var done atomic.Bool
	var subscribed atomic.Bool
	var sent atomic.Bool
	// The rest of the reply is held back until the first words reached the
	// caller, so "streams before completion" never depends on sleeps.
	firstWords := make(chan struct{})
	var firstOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, auth := r.BasicAuth()
		if !auth || user != "nexus" || pass != "test-password" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/session":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"ses_test"}`))
		case "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			// Subscribed before the client can see the sentinel it waits for.
			subscribed.Store(true)
			_, _ = fmt.Fprint(w, "data: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case event := <-events:
					_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case "/session/ses_test/prompt_async":
			if !subscribed.Load() {
				t.Error("prompt started before SSE subscription")
			}
			sent.Store(true)
			go func() {
				events <- `{"type":"message.updated","properties":{"info":{"id":"msg_1","sessionID":"ses_test","role":"assistant"}}}`
				events <- `{"type":"message.part.updated","properties":{"part":{"id":"part_1","sessionID":"ses_test","messageID":"msg_1","type":"text","text":""}}}`
				events <- `{"type":"message.part.delta","properties":{"sessionID":"ses_test","messageID":"msg_1","partID":"part_1","field":"text","delta":"Hel"}}`
				select {
				case <-firstWords:
				case <-time.After(5 * time.Second):
					// The assertions below report the missing early text.
				}
				events <- `{"type":"message.part.delta","properties":{"sessionID":"ses_test","messageID":"msg_1","partID":"part_1","field":"text","delta":"lo"}}`
				done.Store(true)
				events <- `{"type":"session.idle","properties":{"sessionID":"ses_test"}}`
			}()
			w.WriteHeader(http.StatusNoContent)
		case "/session/ses_test/message":
			w.Header().Set("Content-Type", "application/json")
			if done.Load() {
				_, _ = w.Write([]byte(`[{"info":{"id":"msg_1","role":"assistant"},"parts":[{"type":"text","text":"Hello"}]}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case "/session/status":
			w.Header().Set("Content-Type", "application/json")
			if done.Load() {
				_, _ = w.Write([]byte(`{}`))
			} else {
				_, _ = w.Write([]byte(`{"ses_test":{"type":"busy"}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var beforeFinish []string
	result, err := runOpenCodeHTTP(ctx, server.Client(), server.URL, "test-password", openCodeInput{prompt: "Hello"}, func(p StreamProgress) {
		if p.Kind == "text" && !done.Load() {
			beforeFinish = append(beforeFinish, p.Text)
			firstOnce.Do(func() { close(firstWords) })
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sent.Load() || result.NativeID != "ses_test" || result.Text != "Hello" || len(beforeFinish) == 0 || beforeFinish[0] != "Hel" {
		t.Fatalf("stream result=%+v early=%q sent=%t", result, beforeFinish, sent.Load())
	}
}

func TestOpenCodePlanIncludesNativeSessionAndImage(t *testing.T) {
	image := filepath.Join(t.TempDir(), "picture.jpg")
	if err := os.WriteFile(image, []byte("fake jpeg"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := SeededPlan("opencode", "describe", "", []string{image})
	if err != nil {
		t.Fatal(err)
	}
	input, err := openCodeInputFromPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := openCodePromptParts(input)
	if err != nil || len(parts) != 2 || !strings.HasPrefix(parts[1]["url"], "data:image/jpeg;base64,") {
		t.Fatalf("parts=%v err=%v", parts, err)
	}
	native, err := Plan("opencode", "here", "ses_native", "continue")
	if err != nil {
		t.Fatal(err)
	}
	input, err = openCodeInputFromPlan(native)
	if err != nil || input.session != "ses_native" || input.prompt != "continue" {
		t.Fatalf("native input=%+v err=%v", input, err)
	}
}

func TestOpenCodePlanParsesSelectedModel(t *testing.T) {
	plan, err := SeededPlan("opencode", "hello", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = WithModel(plan, "anthropic/claude-sonnet-4")
	if err != nil {
		t.Fatal(err)
	}
	input, err := openCodeInputFromPlan(plan)
	if err != nil || input.model != "anthropic/claude-sonnet-4" || input.prompt != "hello" {
		t.Fatalf("input=%+v err=%v", input, err)
	}
}
