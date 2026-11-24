//go:build livesmoke

package continuex

// Measures whether each live driver streams a long reply incrementally.
// Run: go test -tags livesmoke -run TestStreamLongReply -v ./internal/continuex/

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStreamLongReply(t *testing.T) {
	prompt := "Without using any tools, write the numbers 1 to 80, one per line, each followed by one short English word."
	starters := map[string]func(context.Context, LiveOptions) (LiveSession, error){
		"claude":   StartClaudeLive,
		"codex":    StartCodexLive,
		"cursor":   func(c context.Context, o LiveOptions) (LiveSession, error) { return StartACPLive(c, "cursor", o) },
		"opencode": func(c context.Context, o LiveOptions) (LiveSession, error) { return StartACPLive(c, "opencode", o) },
	}
	for _, name := range []string{"claude", "codex", "cursor", "opencode"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			s, err := starters[name](ctx, LiveOptions{Workspace: t.TempDir()})
			if err != nil {
				t.Skip(err)
			}
			defer s.Close()
			start := time.Now()
			if err := s.Send(ctx, prompt); err != nil {
				t.Fatal(err)
			}
			var deltas, fullTexts, chars int
			var first, last time.Duration
			for {
				select {
				case <-ctx.Done():
					t.Fatalf("timeout: deltas=%d", deltas)
				case ev, ok := <-s.Events():
					if !ok {
						t.Fatalf("closed early: deltas=%d", deltas)
					}
					switch ev.Kind {
					case "text_delta":
						if deltas == 0 {
							first = time.Since(start)
						}
						deltas++
						chars += len(ev.Text)
						last = time.Since(start)
					case "text":
						fullTexts++
						if deltas == 0 {
							chars += len(ev.Text)
						}
					case "approval":
						_ = s.Approve(ev.RequestID, false, "no tools")
					case "turn_done":
						lines := strings.Count(strings.Repeat("x", 0), "\n")
						_ = lines
						t.Logf("%s: deltas=%d fullTexts=%d chars=%d firstDelta=%s lastDelta=%s total=%s",
							name, deltas, fullTexts, chars, first.Round(time.Millisecond), last.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
						return
					}
				}
			}
		})
	}
}
