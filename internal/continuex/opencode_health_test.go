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

func TestOpenCodeErrorEventsAreActionableAndSessionScoped(t *testing.T) {
	cases := []struct{ kind, payload, want string }{
		{"session.error", `{"sessionID":"ses_test","error":{"name":"ModelNotFoundError","data":{"message":"secret-token"}}}`, "model"},
		{"session.error", `{"sessionID":"ses_test","error":{"name":"APIError","data":{"statusCode":401}}}`, "signed in"},
		{"message.updated", `{"info":{"sessionID":"ses_test","error":{"name":"APIError","data":{"statusCode":429}}}}`, "limiting requests"},
		{"session.error", `{"sessionID":"ses_test","error":{"name":"QuotaExceeded"}}`, "quota"},
		{"permission.asked", `{"sessionID":"ses_test"}`, "permission"},
		{"question.asked", `{"sessionID":"ses_test"}`, "question"},
	}
	for _, c := range cases {
		event := openCodeEvent{Type: c.kind, Properties: json.RawMessage(c.payload)}
		err := openCodeEventProblem(event, "ses_test")
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), c.want) || strings.Contains(err.Error(), "secret-token") {
			t.Errorf("%s: %v", c.kind, err)
		}
		if openCodeEventProblem(event, "ses_other") != nil {
			t.Error("another session's failure leaked")
		}
	}
}

func TestOpenCodeModelSelectionRejectsRetiredOrDisconnectedModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/config" {
			fmt.Fprint(w, `{"model":"provider/retired"}`)
			return
		}
		fmt.Fprint(w, `{"all":[{"id":"provider","models":{"current":{}}}],"connected":["provider"]}`)
	}))
	defer server.Close()
	history := []openCodeMessage{{}}
	history[0].Info.Model.ProviderID = "provider"
	history[0].Info.Model.ModelID = "retired"
	for _, selection := range []string{"", "provider/retired", "missing/current"} {
		input := openCodeInput{model: selection}
		if err := validateOpenCodeModel(context.Background(), server.Client(), server.URL, "", &input, history); err == nil {
			t.Errorf("accepted missing model %q", selection)
		}
	}
	input := openCodeInput{model: "provider/current"}
	if err := validateOpenCodeModel(context.Background(), server.Client(), server.URL, "", &input, history); err != nil {
		t.Fatal(err)
	}
	configured := openCodeInput{}
	if err := validateOpenCodeModel(context.Background(), server.Client(), server.URL, "", &configured, nil); err == nil {
		t.Fatal("accepted retired default from config")
	}
}

func TestOpenCodeRuntimeFailureAndNoTextTimeoutStopTheRequest(t *testing.T) {
	for _, mode := range []string{"model-error", "poll-error", "timeout", "partial-poll"} {
		t.Run(mode, func(t *testing.T) {
			var started, aborted atomic.Bool
			var polls atomic.Int32
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
						case <-r.Context().Done():
							return
						}
					}
				case "/session/ses_test/prompt_async":
					started.Store(true)
					if mode == "model-error" {
						events <- `{"type":"session.error","properties":{"sessionID":"ses_test","error":{"name":"ModelNotFoundError"}}}`
					}
					w.WriteHeader(204)
				case "/session/ses_test/abort":
					aborted.Store(true)
					w.WriteHeader(204)
				case "/session/ses_test/message":
					polls.Add(1)
					if mode == "poll-error" && started.Load() {
						fmt.Fprint(w, `[{"info":{"id":"msg_new","role":"assistant","error":{"name":"ProviderAuthError"}},"parts":[]}]`)
					} else if mode == "partial-poll" && started.Load() {
						text := "Hello"
						if polls.Load() > 1 {
							text = "Hello world"
						}
						fmt.Fprintf(w, `[{"info":{"id":"msg_new","role":"assistant"},"parts":[{"type":"text","text":%q}]}]`, text)
					} else {
						fmt.Fprint(w, `[]`)
					}
				case "/session/status":
					if mode == "partial-poll" && polls.Load() > 1 {
						fmt.Fprint(w, `{}`)
					} else {
						fmt.Fprint(w, `{"ses_test":{"type":"busy"}}`)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			seen := []string{}
			out, err := runOpenCodeHTTPWithPolicy(ctx, server.Client(), server.URL, "", openCodeInput{prompt: "hello"}, func(p StreamProgress) {
				if p.Kind == "text" {
					seen = append(seen, p.Text)
				}
			}, openCodeWaitPolicy{poll: 80 * time.Millisecond, firstText: 350 * time.Millisecond})
			if mode == "partial-poll" {
				if err != nil || out.Text != "Hello world" || len(seen) < 2 || seen[0] != "Hello" || aborted.Load() {
					t.Fatalf("partial fallback: %+v %v seen=%v aborted=%v", out, err, seen, aborted.Load())
				}
			} else if err == nil || !aborted.Load() {
				t.Fatalf("%s must fail and abort: %v aborted=%v", mode, err, aborted.Load())
			}
		})
	}
}
