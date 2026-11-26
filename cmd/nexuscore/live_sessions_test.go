//go:build nexuscorelib

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"central-memory/internal/agentrun"
	"central-memory/internal/continuex"
)

type fakeLive struct {
	mu     sync.Mutex
	text   string
	images []string
	sent   chan struct{}
	events chan continuex.LiveEvent
}

func newFakeLive() *fakeLive {
	return &fakeLive{sent: make(chan struct{}, 4), events: make(chan continuex.LiveEvent)}
}
func (f *fakeLive) Send(_ context.Context, text string) error {
	f.mu.Lock()
	f.text = text
	f.mu.Unlock()
	f.sent <- struct{}{}
	return nil
}
func (f *fakeLive) Approve(string, bool, string) error { return nil }
func (f *fakeLive) Interrupt() error                   { return nil }
func (f *fakeLive) Events() <-chan continuex.LiveEvent { return f.events }
func (f *fakeLive) NativeID() string                   { return "" }
func (f *fakeLive) Close() error                       { return nil }

type fakeImageLive struct{ *fakeLive }

func (f *fakeImageLive) SendWithImages(_ context.Context, text string, images []string) error {
	f.mu.Lock()
	f.text, f.images = text, images
	f.mu.Unlock()
	f.sent <- struct{}{}
	return nil
}

func testImage(t *testing.T) string {
	image := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(image, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...), 0600); err != nil {
		t.Fatal(err)
	}
	return image
}

func managerWith(t *testing.T, session continuex.LiveSession) *agentrun.Manager {
	m := agentrun.New(agentrun.Config{
		Start: func(context.Context, agentrun.StartSpec) (continuex.LiveSession, error) {
			return ownLiveSession(session, ""), nil
		},
		Record: func(agentrun.TurnRecord) (any, error) { return nil, nil },
	})
	t.Cleanup(m.Shutdown)
	return m
}

func TestLiveSendPassesImagesAndRecordsTheirNames(t *testing.T) {
	image := testImage(t)
	session := &fakeImageLive{newFakeLive()}
	m := managerWith(t, session)
	req := agentrun.SendRequest{SessionID: "s1", Agent: "claude", Workspace: t.TempDir(), Text: "what is this?", Images: []string{image}}
	if err := validateLiveSend(&req); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Send(req); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.sent:
	case <-time.After(3 * time.Second):
		t.Fatal("not sent")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.text != "what is this?" || len(session.images) != 1 || session.images[0] != image {
		t.Fatalf("agent got %q %v", session.text, session.images)
	}
	if prompt := continuex.ImageTags("what is this?", []string{image}); !strings.Contains(prompt, "<image name=[Image #1]") {
		t.Fatalf("recorded prompt %q", prompt)
	}
}

func TestLiveSendRefusesImagesTheAgentCannotTake(t *testing.T) {
	image := testImage(t)
	m := managerWith(t, newFakeLive())
	req := agentrun.SendRequest{SessionID: "s2", Agent: "gemini", Workspace: t.TempDir(), Text: "hi", Images: []string{image}}
	if err := validateLiveSend(&req); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Send(req); err == nil || !strings.Contains(err.Error(), "can't take images") {
		t.Fatalf("want a clear refusal, got %v", err)
	}
	bad := agentrun.SendRequest{SessionID: "s3", Agent: "gemini", Workspace: t.TempDir(), Text: "hi", Images: []string{filepath.Join(t.TempDir(), "x.exe")}}
	if err := validateLiveSend(&bad); err == nil {
		t.Fatal("a non-image path must be refused")
	}
	missing := agentrun.SendRequest{SessionID: "s4", Agent: "claude", Workspace: filepath.Join(t.TempDir(), "nope"), Text: "hi"}
	if err := validateLiveSend(&missing); err == nil {
		t.Fatal("a missing folder must be refused")
	}
}
