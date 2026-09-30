package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"central-memory/internal/core"
	"central-memory/internal/outbox"
)

type captureOptions struct {
	Foreground bool
	OutboxDir  string
}

func parseCaptureArgs(args []string) (captureOptions, error) {
	var o captureOptions
	fs := flag.NewFlagSet("nexus capture", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.Foreground, "foreground", false, "run headless capture in the foreground (no TCP)")
	fs.StringVar(&o.OutboxDir, "outbox", "", "upload outbox directory (default: temp)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if len(fs.Args()) > 0 {
		return o, fmt.Errorf("capture takes no positional arguments, got %q", strings.Join(fs.Args(), " "))
	}
	return o, nil
}

// runCapture is the headless capture entry (spec 7.8 step 6).
// It never opens :7272 — daemon TCP is retired in favor of embedded core.
func runCapture(_ Config, args []string, stdout io.Writer) error {
	o, err := parseCaptureArgs(args)
	if err != nil {
		return err
	}
	if !o.Foreground {
		return fmt.Errorf("usage: nexus capture --foreground\n\n" +
			"daemon TCP (:7272) is retired; headless capture runs in-process with an upload outbox.\n" +
			"For app-driven capture use the embedded nexuscore engine")
	}

	_ = os.Setenv("NEXUS_NO_TCP", "1")
	if err := core.RefuseListen(); err != nil {
		fmt.Fprintf(stdout, "nexus capture: no TCP listen (%v)\n", err)
	}

	dir := strings.TrimSpace(o.OutboxDir)
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "nexus-capture-outbox")
	}
	sp, err := outbox.Open(dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "nexus capture --foreground: outbox at %s (pending=%d)\n", dir, len(sp.Pending()))
	fmt.Fprintln(stdout, "placeholder: harvester wires into outbox in a later P0 slice; refusing :7272")

	// NEXUS_CAPTURE_ONCE=1: setup-only (tests / CI smoke).
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("NEXUS_CAPTURE_ONCE"))); v == "1" || v == "true" {
		fmt.Fprintln(stdout, "nexus capture: once mode, exiting")
		return nil
	}

	fmt.Fprintln(stdout, "ctrl-c to stop")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stdout, "nexus capture: stopped")
			return nil
		case <-ticker.C:
			fmt.Fprintf(stdout, "nexus capture: heartbeat pending=%d\n", len(sp.Pending()))
		}
	}
}
