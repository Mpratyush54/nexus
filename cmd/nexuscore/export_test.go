//go:build nexuscorelib

package main

import (
	"os"
	"path/filepath"
	"testing"

	"central-memory/internal/daemon"
)

func TestEmbeddedWorkspaceRootRequiresRealNonHomeDirectory(t *testing.T) {
	root := t.TempDir()
	if got := embeddedWorkspaceRoot(root); got != filepath.Clean(root) {
		t.Fatalf("root = %q, want %q", got, filepath.Clean(root))
	}
	if got := embeddedWorkspaceRoot(filepath.Join(root, "missing")); got != "" {
		t.Fatalf("missing root = %q", got)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if got := embeddedWorkspaceRoot(home); got != "" {
			t.Fatalf("home root = %q", got)
		}
	}
}

func TestEmbeddedCapturePauseResumeControlsRuntime(t *testing.T) {
	defer func() {
		nxState.mu.Lock()
		nxState.captureCancel = nil
		nxState.captureDone = nil
		nxState.captureRuntime = nil
		nxState.captureConfig = nil
		nxState.capturePaused = false
		nxState.captureStarting = false
		nxState.mu.Unlock()
	}()

	nxState.mu.Lock()
	nxState.captureGeneration++
	root := t.TempDir()
	token, err := daemon.GenerateToken()
	if err != nil {
		nxState.mu.Unlock()
		t.Fatal(err)
	}
	d, err := daemon.NewDaemon(root, token)
	if err != nil {
		nxState.mu.Unlock()
		t.Fatal(err)
	}
	runtime := daemon.NewRuntime(d, filepath.Base(root), daemon.NewStaticDesignation(false))
	nxState.captureConfig = &embeddedCaptureConfig{
		generation: nxState.captureGeneration,
		root:       root,
	}
	nxState.captureCancel = func() {}
	nxState.captureRuntime = runtime
	nxState.capturePaused = false
	nxState.mu.Unlock()

	controller := embeddedCaptureController{}
	if err := controller.Pause(); err != nil {
		t.Fatal(err)
	}
	paused := controller.Status().(map[string]any)
	if paused["running"] != true || paused["paused"] != true || !runtime.Paused() {
		t.Fatalf("unexpected paused status: %#v", paused)
	}
	if err := controller.Resume(); err != nil {
		t.Fatal(err)
	}
	resumed := controller.Status().(map[string]any)
	if resumed["running"] != true || resumed["paused"] != false || runtime.Paused() {
		t.Fatalf("unexpected resumed status: %#v", resumed)
	}
}
