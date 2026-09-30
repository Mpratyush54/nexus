package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseCaptureArgs(t *testing.T) {
	o, err := parseCaptureArgs([]string{"--foreground", "--outbox", "/tmp/o"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Foreground || o.OutboxDir != "/tmp/o" {
		t.Fatalf("%+v", o)
	}
	if _, err := parseCaptureArgs([]string{"nope"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCaptureWithoutForegroundErrors(t *testing.T) {
	var buf bytes.Buffer
	err := runCapture(Config{}, nil, &buf)
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("err=%v", err)
	}
}

func TestCaptureForegroundOnce(t *testing.T) {
	t.Setenv("NEXUS_CAPTURE_ONCE", "1")
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := runCapture(Config{}, []string{"--foreground", "--outbox", dir}, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "refusing :7272") || !strings.Contains(out, "outbox at") {
		t.Fatalf("output=%q", out)
	}
	if !strings.Contains(out, "no TCP listen") {
		t.Fatalf("missing no-tcp line: %q", out)
	}
}

func TestCaptureForegroundDemoEnqueue(t *testing.T) {
	t.Setenv("NEXUS_CAPTURE_ONCE", "1")
	t.Setenv("NEXUS_CAPTURE_DEMO_ENQUEUE", "1")
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := runCapture(Config{}, []string{"--foreground", "--outbox", dir}, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "DemoEnqueue") {
		t.Fatalf("expected DemoEnqueue log: %q", out)
	}
}
