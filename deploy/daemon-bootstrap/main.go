// Command daemon-bootstrap is the production entrypoint for the workspace
// daemon container (issue #20, plan §1.3).
//
// It boots the local-first daemon from internal/daemon against a workspace
// root: loads (or mints, mode 0600) the bearer token, serves the sandboxed
// file/git/command API, registers with the central server, and heartbeats
// every 30s. Empty CENTRAL_SERVER_URL means local-only mode (register and
// heartbeat are no-ops) — the container still serves /healthz unauthenticated
// for the orchestrator probe.
//
// Environment:
//
//	WORKSPACE_ROOT       workspace directory (default /workspace)
//	CENTRAL_SERVER_URL   e.g. https://api.example.com (optional)
//	DAEMON_ADDR          listen address (default :7687)
//	MACHINE_ID           override for os.Hostname() (optional)
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"central-memory/internal/daemon"
)

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("daemon-bootstrap: %v", err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	root := getenv("WORKSPACE_ROOT", "/workspace")
	serverURL := strings.TrimSpace(os.Getenv("CENTRAL_SERVER_URL"))
	addr := getenv("DAEMON_ADDR", ":7687")

	d, err := daemon.New(root, serverURL, addr)
	if err != nil {
		return err
	}
	if mid := strings.TrimSpace(os.Getenv("MACHINE_ID")); mid != "" {
		d.MachineID = mid
	}

	bound, err := d.Start(ctx)
	if err != nil {
		return err
	}
	log.Printf("daemon-bootstrap: serving workspace root=%s addr=%s machine=%s server=%q",
		d.Root, bound, d.MachineID, d.ServerURL)

	if d.ServerURL != "" {
		if err := d.Register(ctx); err != nil {
			// Registration failure must not kill the daemon: local file/git
			// ops stay available and the heartbeat loop retries presence.
			log.Printf("daemon-bootstrap: initial register failed (will retry via heartbeat): %v", err)
		}
		d.StartHeartbeatLoop(ctx)
		defer d.StopHeartbeat()
	} else {
		log.Print("daemon-bootstrap: CENTRAL_SERVER_URL empty — local-only mode (no register/heartbeat)")
	}

	<-ctx.Done()
	log.Print("daemon-bootstrap: shutdown signal received, draining")
	d.Wait()
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	return nil
}
