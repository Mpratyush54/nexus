package continuex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestControllableRecordsArgv(t *testing.T) {
	r := &Controllable{NextID: "op_fixed"}
	plan, err := Plan("claude", "open_in_agent", "ses_1", "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := r.Start(context.Background(), plan)
	if err != nil || id != "op_fixed" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	got := r.LastArgv()
	if len(got) == 0 || got[0] != "claude" {
		t.Fatalf("argv=%v", got)
	}
	if len(r.Calls) != 1 || r.Calls[0].Mode != "open_in_agent" {
		t.Fatalf("calls=%+v", r.Calls)
	}
	r.Fail = errors.New("boom")
	if _, err := r.Start(context.Background(), plan); err == nil {
		t.Fatal("expected fail")
	}
}

func TestExecRunnerWithFakeBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-agent")
	if runtime.GOOS == "windows" {
		bin += ".bat"
		if err := os.WriteFile(bin, []byte("@echo off\r\nexit /b 0\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	r := &ExecRunner{Wait: true}
	id, err := r.Start(context.Background(), LaunchPlan{
		Agent: "fake", Mode: "here", Argv: []string{"fake-agent", "--resume", "ses_x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || id[:3] != "op_" {
		t.Fatalf("op_id=%q", id)
	}
}
