package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"central-memory/internal/config"
	"central-memory/internal/daemon"
)

func runRestore(ctx context.Context, cfg Config, o sessionOptions, stdout io.Writer) error {
	ws := strings.TrimSpace(o.Workspace)
	if ws == "" {
		return fmt.Errorf("session restore requires --workspace / -w")
	}
	server := firstNonEmpty(cfg.ServerURL, config.ResolveServerURL(""))
	token := firstNonEmpty(cfg.Token, config.ResolveToken())
	msg, err := daemon.RestoreSession(ctx, daemon.RestoreOptions{
		SessionID: o.ID,
		Workspace: ws,
		Harness:   o.Harness,
		DryRun:    o.DryRun,
		Force:     o.Force,
		ServerURL: server,
		Token:     token,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, msg)
	return nil
}
