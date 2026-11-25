//go:build nexuscorelib

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"central-memory/internal/continuex"
)

type fakeLive struct {
	text   string
	images []string
}

func (f *fakeLive) Send(_ context.Context, text string) error { f.text = text; return nil }
func (f *fakeLive) Approve(string, bool, string) error        { return nil }
func (f *fakeLive) Interrupt() error                          { return nil }
func (f *fakeLive) Events() <-chan continuex.LiveEvent        { return nil }
func (f *fakeLive) NativeID() string                          { return "" }
func (f *fakeLive) Close() error                              { return nil }

type fakeImageLive struct{ fakeLive }

func (f *fakeImageLive) SendWithImages(_ context.Context, text string, images []string) error {
	f.text, f.images = text, images
	return nil
}

func TestLiveSendPassesImagesAndRecordsTheirNames(t *testing.T) {
	image := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(image, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...), 0600); err != nil {
		t.Fatal(err)
	}
	session := &fakeImageLive{}
	entry := &liveEntry{id: "live-img", agent: "claude", session: session}
	liveSessions.Store(entry.id, entry)
	defer liveSessions.Delete(entry.id)
	args, _ := json.Marshal(map[string]any{"live_id": entry.id, "text": "what is this?", "images": []string{image}})
	if _, err := liveCall("live.send", args); err != nil {
		t.Fatal(err)
	}
	if session.text != "what is this?" || len(session.images) != 1 || session.images[0] != image {
		t.Fatalf("agent got %q %v", session.text, session.images)
	}
	if recorded := entry.prompt.String(); !strings.Contains(recorded, "<image name=[Image #1]") || !strings.HasPrefix(recorded, "what is this?") {
		t.Fatalf("recorded prompt %q", recorded)
	}
}

func TestLiveSendRefusesImagesTheAgentCannotTake(t *testing.T) {
	image := filepath.Join(t.TempDir(), "shot.png")
	_ = os.WriteFile(image, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...), 0600)
	entry := &liveEntry{id: "live-noimg", agent: "gemini", session: &fakeLive{}}
	liveSessions.Store(entry.id, entry)
	defer liveSessions.Delete(entry.id)
	args, _ := json.Marshal(map[string]any{"live_id": entry.id, "text": "hi", "images": []string{image}})
	if _, err := liveCall("live.send", args); err == nil || !strings.Contains(err.Error(), "can't take images") {
		t.Fatalf("want a clear refusal, got %v", err)
	}
	args, _ = json.Marshal(map[string]any{"live_id": entry.id, "text": "hi", "images": []string{filepath.Join(t.TempDir(), "x.exe")}})
	if _, err := liveCall("live.send", args); err == nil {
		t.Fatal("a non-image path must be refused")
	}
}
