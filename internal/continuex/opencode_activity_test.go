package continuex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenCodeActivityIsScopedDeduplicatedAndSeparateFromText(t *testing.T) {
	var progress []StreamProgress
	tracker := newOpenCodeTextTracker("ses_ours", func(p StreamProgress) { progress = append(progress, p) })
	tracker.ignore["msg_old"] = true
	rows := []string{
		`{"type":"message.updated","properties":{"info":{"id":"msg_old","sessionID":"ses_ours","role":"assistant"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"p_old","sessionID":"ses_ours","messageID":"msg_old","type":"tool","tool":"PRIVATE","state":{"status":"completed"}}}}`,
		`{"type":"message.updated","properties":{"info":{"id":"msg_user","sessionID":"ses_ours","role":"user"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"p_user","sessionID":"ses_ours","messageID":"msg_user","type":"tool","tool":"PRIVATE","state":{"status":"completed"}}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"p_other","sessionID":"ses_other","messageID":"msg_new","type":"tool","tool":"PRIVATE","state":{"status":"completed"}}}}`,
		// Exercise part-before-role event ordering.
		`{"type":"message.part.updated","properties":{"part":{"id":"p_tool","sessionID":"ses_ours","messageID":"msg_new","type":"tool","tool":"edit","state":{"status":"running","input":{"filePath":"app.go","token":"SECRET"},"output":"Edited app.go"}}}}`,
		`{"type":"message.updated","properties":{"info":{"id":"msg_new","sessionID":"ses_ours","role":"assistant"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"p_reason","sessionID":"ses_ours","messageID":"msg_new","type":"reasoning","text":"SECRET REASONING"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"p_tool","sessionID":"ses_ours","messageID":"msg_new","type":"tool","tool":"edit","state":{"status":"completed","input":{"filePath":"app.go"}}}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"p_tool","sessionID":"ses_ours","messageID":"msg_new","type":"tool","tool":"edit","state":{"status":"completed","input":{"filePath":"app.go"}}}}}`,
	}
	for _, raw := range rows {
		var event openCodeEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		tracker.accept(event)
	}
	if len(progress) != 2 || !tracker.active || tracker.text() != "" || len(tracker.tools) != 1 {
		t.Fatalf("activity tracking: %#v", progress)
	}
	for _, p := range progress {
		if p.Kind != "activity" || strings.Contains(p.Text, "SECRET") || strings.Contains(p.Text, "PRIVATE") || !strings.Contains(p.Text, "app.go") {
			t.Fatalf("unsafe progress: %#v", p)
		}
	}
}

func TestOpenCodePollRetainsMultiStepTextAndIgnoresHistory(t *testing.T) {
	tracker := newOpenCodeTextTracker("ses_test", nil)
	tracker.ignore["old"] = true
	var messages []openCodeMessage
	if err := json.Unmarshal([]byte(`[
	 {"info":{"id":"old","role":"assistant"},"parts":[{"id":"oldtext","type":"text","text":"OLD PRIVATE"}]},
	 {"info":{"id":"m1","role":"assistant"},"parts":[{"id":"p1","type":"text","text":"First step"}]},
	 {"info":{"id":"m2","role":"assistant"},"parts":[{"id":"p2","type":"tool","tool":"read","state":{"status":"completed"}}]},
	 {"info":{"id":"m3","role":"assistant"},"parts":[{"id":"p3","type":"text","text":"Final reply"}]}]`), &messages); err != nil {
		t.Fatal(err)
	}
	tracker.observeMessages(messages, "old")
	tracker.observeMessages(messages[2:], "old") // A limited poll no longer includes the initial text part.
	if tracker.answer() != "First step\n\nFinal reply" {
		t.Fatalf("lost multi-step text: %q", tracker.answer())
	}
	if text, _ := latestOpenCodeAnswer(messages, "old"); text != tracker.answer() {
		t.Fatalf("final differs: %q", text)
	}
}

func TestOpenCodePollAheadOfSSEDoesNotDuplicateQueuedTokens(t *testing.T) {
	tracker := newOpenCodeTextTracker("ses_test", nil)
	rows := []string{
		`{"type":"message.updated","properties":{"info":{"id":"msg_1","sessionID":"ses_test","role":"assistant"}}}`,
		`{"type":"message.part.updated","properties":{"part":{"id":"part_1","sessionID":"ses_test","messageID":"msg_1","type":"text","text":""}}}`,
		`{"type":"message.part.delta","properties":{"sessionID":"ses_test","messageID":"msg_1","partID":"part_1","field":"text","delta":"Hel"}}`,
	}
	accept := func(raw string) {
		var event openCodeEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		tracker.accept(event)
	}
	for _, raw := range rows {
		accept(raw)
	}
	var messages []openCodeMessage
	if err := json.Unmarshal([]byte(`[{"info":{"id":"msg_1","role":"assistant"},"parts":[{"id":"part_1","type":"text","text":"Hello"}]}]`), &messages); err != nil {
		t.Fatal(err)
	}
	tracker.observeMessages(messages, "")
	accept(`{"type":"message.part.delta","properties":{"sessionID":"ses_test","messageID":"msg_1","partID":"part_1","field":"text","delta":"lo"}}`)
	if tracker.answer() != "Hello" {
		t.Fatalf("duplicated queued tokens: %q", tracker.answer())
	}
}

func TestOpenCodeToolWorkSurvivesInitialTextDeadline(t *testing.T) {
	for _, mode := range []string{"poll-only", "sse", "stream-drop", "tools-without-prose"} {
		t.Run(mode, func(t *testing.T) {
			var polls atomic.Int32
			var aborted atomic.Bool
			events := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/session":
					fmt.Fprint(w, `{"id":"ses_test"}`)
				case "/event":
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"server.connected\"}\n\n")
					w.(http.Flusher).Flush()
					for {
						select {
						case event := <-events:
							fmt.Fprintf(w, "data: %s\n\n", event)
							w.(http.Flusher).Flush()
							if mode == "stream-drop" && strings.Contains(event, "message.part") {
								return
							}
						case <-r.Context().Done():
							return
						}
					}
				case "/session/ses_test/prompt_async":
					if mode != "poll-only" {
						events <- `{"type":"message.updated","properties":{"info":{"id":"msg_work","role":"assistant","sessionID":"ses_test"}}}`
						events <- `{"type":"message.part.updated","properties":{"part":{"id":"tool1","messageID":"msg_work","sessionID":"ses_test","type":"tool","tool":"edit","state":{"status":"running","input":{"filePath":"app.go"}}}}}`
					}
					w.WriteHeader(204)
				case "/session/ses_test/message":
					n := polls.Add(1)
					status, text := "running", ""
					if n >= 10 {
						status = "completed"
						if mode != "tools-without-prose" {
							text = `,{"id":"text1","type":"text","text":"Files updated"}`
						}
					}
					fmt.Fprintf(w, `[{"info":{"id":"msg_work","role":"assistant"},"parts":[{"id":"tool1","type":"tool","tool":"edit","state":{"status":%q,"input":{"filePath":"app.go"}}}%s]}]`, status, text)
				case "/session/status":
					if polls.Load() >= 12 {
						fmt.Fprint(w, `{}`)
					} else {
						fmt.Fprint(w, `{"ses_test":{"type":"busy"}}`)
					}
				case "/session/ses_test/abort":
					aborted.Store(true)
					w.WriteHeader(204)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			var earlyActivity, earlyText bool
			out, err := runOpenCodeHTTPWithPolicy(ctx, server.Client(), server.URL, "", openCodeInput{prompt: "edit"}, func(p StreamProgress) {
				if p.Kind == "activity" && polls.Load() < 10 {
					earlyActivity = true
				}
				if p.Kind == "text" && polls.Load() < 12 {
					earlyText = true
				}
			}, openCodeWaitPolicy{poll: 20 * time.Millisecond, firstText: 70 * time.Millisecond})
			if err != nil || aborted.Load() || !earlyActivity || out.NativeID != "ses_test" {
				t.Fatalf("result=%+v err=%v aborted=%v earlyActivity=%v", out, err, aborted.Load(), earlyActivity)
			}
			if mode == "tools-without-prose" {
				if !strings.Contains(out.Text, "Nexus execution record") || !strings.Contains(out.Text, "1 completed") {
					t.Fatalf("missing honest tool outcome: %q", out.Text)
				}
			} else if out.Text != "Files updated" || !earlyText {
				t.Fatalf("text not incremental: %+v early=%v", out, earlyText)
			}
		})
	}
}
