// Command daemon — local-dev entrypoint for the workspace daemon
// (issue #39, plan target tree).
//
// Flag-driven thin wiring only: -root selects the workspace directory,
// -port the listen port, -server the central server base URL (empty means
// local-only mode: register/heartbeat are no-ops). Builds via daemon.New,
// serves via Daemon.Start, registers + heartbeats when a server is set.
// This is the local counterpart to deploy/daemon-bootstrap (the container
// entrypoint, env-fixed to /workspace + :7687) — that file is untouched.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"central-memory/internal/daemon"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatalf("daemon: %v", err)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	root := fs.String("root", ".", "workspace root directory")
	port := fs.Int("port", 7687, "listen port on 127.0.0.1")
	serverURL := fs.String("server", os.Getenv("CENTRAL_SERVER_URL"), "central server base URL (empty = local-only mode, no register/heartbeat)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *port <= 0 || *port > 65535 {
		return fmt.Errorf("daemon: invalid port %d (want 1-65535)", *port)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", *port)

	token, err := daemon.EnsureToken(*root)
	if err != nil {
		return err
	}
	d, err := daemon.NewDaemon(*root, token)
	if err != nil {
		return err
	}
	d.ServerURL = *serverURL

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- d.Start(addr)
	}()

	if d.ServerURL != "" {
		if err := d.Register(ctx, d.ServerURL); err != nil {
			log.Printf("daemon: initial register failed: %v", err)
		}
		hbCtx, hbCancel := context.WithCancel(context.Background())
		defer hbCancel()
		go d.StartHeartbeat(hbCtx, d.ServerURL, daemon.HeartbeatInterval)
	} else {
		log.Print("daemon: no server set — local-only mode (no register/heartbeat)")
	}

	log.Printf("daemon: serving workspace root=%s addr=%s machine=%s server=%q",
		d.Root, addr, d.MachineID, d.ServerURL)

	select {
	case <-ctx.Done():
		log.Print("daemon: shutdown signal received, draining")
	case err := <-serveErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}
	if err := d.Close(); err != nil {
		return err
	}
	if err := <-serveErr; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
