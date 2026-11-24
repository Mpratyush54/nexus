package continuex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenCodeRetryEventsAreSessionScopedAndBounded(t *testing.T) {
	next := time.Now().Add(10*time.Hour + 11*time.Minute).UnixMilli()
	props, _ := json.Marshal(map[string]any{"sessionID": "ses_test", "status": openCodeSessionStatus{Type: "retry", Attempt: 1, Message: "Free usage exceeded, subscribe to Go", Next: next}})
	event := openCodeEvent{Type: "session.status", Properties: props}
	err := openCodeEventProblem(event, "ses_test")
	if err == nil || !strings.Contains(err.Error(), "quota") || !strings.Contains(err.Error(), "10h") {
		t.Fatalf("quota retry ignored: %v", err)
	}
	if openCodeEventProblem(event, "ses_other") != nil {
		t.Fatal("another session's retry leaked")
	}
	part := openCodeEvent{Type: "message.part.updated", Properties: json.RawMessage(`{"part":{"id":"p1","messageID":"m1","sessionID":"ses_test","type":"retry","attempt":1,"error":{"name":"APIError","data":{"message":"Free usage exceeded, subscribe to Go"}}}}`)}
	if err := openCodeEventProblem(part, "ses_test"); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("retry part ignored: %v", err)
	}
	if openCodeEventProblem(part, "ses_other") != nil {
		t.Fatal("another session's retry part leaked")
	}
	if openCodeEventProblem(part, "ses_test", map[string]bool{"m1": true}) != nil {
		t.Fatal("historical SSE retry affected a new turn")
	}
	var recorded []openCodeMessage
	if err := json.Unmarshal([]byte(`[{"info":{"id":"old","role":"assistant"},"parts":[{"id":"retry","type":"retry","attempt":1,"error":{"message":"Free usage exceeded, subscribe to Go"}}]}]`), &recorded); err != nil {
		t.Fatal(err)
	}
	if newOpenCodeMessageError(recorded, "") == nil {
		t.Fatal("polling discarded retry-part quota error")
	}
	if newOpenCodeMessageError(recorded, "old") != nil {
		t.Fatal("historical retry error affected new turn")
	}
	status, err := openCodeStatusHealth(openCodeSessionStatus{Type: "retry", Attempt: 1, Message: "temporary network problem", Next: time.Now().Add(5 * time.Second).UnixMilli()}, time.Now())
	if err != nil || !strings.Contains(status, "retry 1/2") {
		t.Fatalf("short retry failed: %s %v", status, err)
	}
	if _, err := openCodeStatusHealth(openCodeSessionStatus{Type: "retry", Attempt: 3, Next: time.Now().Add(time.Second).UnixMilli()}, time.Now()); err == nil {
		t.Fatal("unbounded retries accepted")
	}
}

func TestOpenCodeHTTPStopsQuotaAndLongRetriesOnSSEAndPolling(t *testing.T) {
	for _, mode := range []string{"sse-quota", "poll-quota", "poll-long", "poll-auth", "short-recover"} {
		t.Run(mode, func(t *testing.T) {
			var aborted atomic.Bool
			var polls atomic.Int32
			events := make(chan string, 4)
			retry := openCodeSessionStatus{Type: "retry", Attempt: 1, Message: "Free usage exceeded, subscribe to Go", Next: time.Now().Add(10*time.Hour + 11*time.Minute).UnixMilli()}
			if mode == "poll-long" {
				retry.Message = "temporary service problem"
			}
			if mode == "poll-auth" {
				retry.Message = "Provider authentication failed"
			}
			if mode == "short-recover" {
				retry.Message = "temporary connection problem"
				retry.Next = time.Now().Add(5 * time.Second).UnixMilli()
			}
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
						case <-r.Context().Done():
							return
						}
					}
				case "/session/ses_test/prompt_async":
					if mode == "sse-quota" {
						raw, _ := json.Marshal(map[string]any{"type": "session.status", "properties": map[string]any{"sessionID": "ses_test", "status": retry}})
						events <- string(raw)
					}
					w.WriteHeader(204)
				case "/session/ses_test/abort":
					aborted.Store(true)
					w.WriteHeader(204)
				case "/session/ses_test/message":
					if polls.Add(1) >= 4 && mode == "short-recover" {
						fmt.Fprint(w, `[{"info":{"id":"m1","role":"assistant"},"parts":[{"id":"p1","type":"text","text":"Recovered"}]}]`)
					} else {
						fmt.Fprint(w, `[]`)
					}
				case "/session/status":
					if polls.Load() >= 4 && mode == "short-recover" {
						fmt.Fprint(w, `{}`)
					} else {
						json.NewEncoder(w).Encode(map[string]openCodeSessionStatus{"ses_test": retry})
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var retryVisible bool
			started := time.Now()
			out, err := runOpenCodeHTTPWithPolicy(ctx, server.Client(), server.URL, "", openCodeInput{prompt: "hello"}, func(p StreamProgress) {
				if p.Kind == "status" && strings.Contains(p.Text, "retry 1/2") {
					retryVisible = true
				}
			}, openCodeWaitPolicy{poll: 30 * time.Millisecond, firstText: time.Second})
			if mode == "short-recover" {
				if err != nil || aborted.Load() || out.Text != "Recovered" || !retryVisible {
					t.Fatalf("short retry lost: %+v %v retryVisible=%v", out, err, retryVisible)
				}
			} else {
				var problem *harnessFailure
				if !errors.As(err, &problem) || !aborted.Load() || time.Since(started) > 2*time.Second {
					t.Fatalf("retry not stopped promptly: %v aborted=%v elapsed=%s", err, aborted.Load(), time.Since(started))
				}
			}
		})
	}
}
