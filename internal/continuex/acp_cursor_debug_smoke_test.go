//go:build livesmoke

package continuex

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Reproduces the in-app Cursor run: model from the picker plus seeded context.
func TestCursorInAppOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts LiveOptions
	}{
		{"model-auto", LiveOptions{Model: "auto"}},
		{"seeded-context", LiveOptions{SystemContext: "user: earlier question\n\nassistant: earlier answer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			tc.opts.Workspace = t.TempDir()
			s, err := StartACPLive(ctx, "cursor", tc.opts)
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			defer s.Close()
			_ = s.Send(ctx, "Reply with just the word hello.")
			var text strings.Builder
			for ev := range s.Events() {
				if ev.Kind != "text_delta" {
					t.Logf("%s: %.300q", ev.Kind, ev.Text)
				} else {
					text.WriteString(ev.Text)
				}
				if ev.Kind == "turn_done" || ev.Kind == "exited" {
					break
				}
			}
			t.Logf("reply: %q", text.String())
		})
	}
}

func TestCursorStaleResumeFallsBack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := StartACPLive(ctx, "cursor", LiveOptions{Workspace: t.TempDir(), Model: "auto",
		ResumeID: "dcea03c4-033e-44c6-9c65-3c7ac11704cd", SystemContext: "user: my favourite colour is teal\n\nassistant: noted"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Close()
	_ = s.Send(ctx, "What is my favourite colour? Answer in one word.")
	var text strings.Builder
	for ev := range s.Events() {
		if ev.Kind == "text_delta" {
			text.WriteString(ev.Text)
			continue
		}
		t.Logf("%s: %.200q", ev.Kind, ev.Text)
		if ev.Kind == "turn_done" || ev.Kind == "exited" {
			break
		}
	}
	t.Logf("reply: %q", text.String())
	if !strings.Contains(strings.ToLower(text.String()), "teal") {
		t.Fatalf("context not carried into the fresh session: %q", text.String())
	}
}
