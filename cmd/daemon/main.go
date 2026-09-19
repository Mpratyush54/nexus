// Command daemon — local-dev entrypoint for the workspace daemon
// (issue #39, plan target tree).
//
// Flag-driven thin wiring only: -root selects the workspace directory,
// -bind the listen address (default 127.0.0.1; use 0.0.0.0 in containers,
// issue #126), -port the listen port, -server the central server base URL
// (empty means local-only mode: register/heartbeat are no-ops), -project
// the extraction project name (default: workspace folder base). Builds via
// daemon.New, serves via Daemon.Start, registers + heartbeats when a server
// is set, and starts the background extraction Runtime (Harvester Layer 2 +
// Watcher Layer 3 + Memory Processor designated-gated, issues #99/#115)
// plus the push-file Materializer (issue #81, daemon-backed I/O).
//
// Phase 9 (issue #169): also serves the browser CORS proxy on
// 127.0.0.1:7272 and resolves -server via the 4-tier cascade
// (flag → CENTRAL_SERVER_URL/NEXUS_SERVER → config file → defaultServerURL).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"central-memory/internal/buildinfo"
	"central-memory/internal/config"
	"central-memory/internal/daemon"
	"central-memory/internal/materializer"
)

// defaultServerURL is the compile-time ServerURL fallback (Phase 9).
// Override at build time:
//
//	go build -ldflags "-X main.defaultServerURL=https://api-nexus.pratyushes.dev" ./cmd/daemon
var defaultServerURL = "https://api-nexus.pratyushes.dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatalf("daemon: %v", err)
	}
}

// defaultBind resolves the -bind default: $DAEMON_BIND wins, else 127.0.0.1
// (local-first; containers pass -bind 0.0.0.0, issue #126).
func defaultBind() string {
	if v := strings.TrimSpace(os.Getenv("DAEMON_BIND")); v != "" {
		return v
	}
	return "127.0.0.1"
}

// defaultPort resolves the -port default: $DAEMON_PORT wins when numeric,
// else 7687. Invalid env values fall back to 7687 (flag validation still
// rejects out-of-range explicit flags).
func defaultPort() int {
	if v := strings.TrimSpace(os.Getenv("DAEMON_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 65535 {
			return n
		}
	}
	return 7687
}

// designatedFromEnv reports whether this daemon may process now (issue #115).
// Explicit CENTRAL_DESIGNATED_PROCESSOR=1/true/yes opts in; 0/false/no opts out.
// When unset, returns false here — main enables auto-sync when a server token
// is configured on the process (flag/env), so leftover config files do not
// flip designation in tests.
func designatedFromEnv() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("CENTRAL_DESIGNATED_PROCESSOR")))
	switch v {
	case "0", "false", "no", "off":
		return false
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func autoDesignated(serverToken string) bool {
	if v := strings.TrimSpace(os.Getenv("CENTRAL_DESIGNATED_PROCESSOR")); v != "" {
		return designatedFromEnv()
	}
	// Product default: harvest → portal when authenticated to a central server.
	return strings.TrimSpace(serverToken) != ""
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "-version" || args[0] == "--version") {
		fmt.Printf("nexus-daemon %s", buildinfo.Version)
		if buildinfo.Commit != "" && buildinfo.Commit != "unknown" {
			fmt.Printf(" (%s)", buildinfo.Commit)
		}
		fmt.Println()
		return nil
	}
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	root := fs.String("root", ".", "workspace root directory")
	bind := fs.String("bind", defaultBind(), "bind address (127.0.0.1 local, 0.0.0.0 in containers)")
	port := fs.Int("port", defaultPort(), "listen port")
	// Tier 1 is the -server flag; its default is tiers 2–4 (env → config → compile).
	serverURL := fs.String("server", config.ResolveServerURL(defaultServerURL), "central server base URL (empty = local-only mode, no register/heartbeat)")
	serverToken := fs.String("server-token", firstNonEmpty(os.Getenv("CENTRAL_SERVER_TOKEN"), config.ResolveToken()), "JWT bearer token for central-server calls")
	userID := fs.String("user", firstNonEmpty(os.Getenv("CENTRAL_USER_ID"), config.ResolveUserID()), "user id (from nexus login / config)")
	project := fs.String("project", strings.TrimSpace(os.Getenv("CENTRAL_PROJECT")), "project name for extraction (default: workspace folder base)")
	proxyAddr := fs.String("proxy", daemon.ResolveProxyAddr(), "browser CORS proxy listen address (empty disables; env DAEMON_PROXY)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if strings.TrimSpace(*bind) == "" {
		return fmt.Errorf("daemon: empty bind address (want 127.0.0.1 or 0.0.0.0)")
	}
	if *port <= 0 || *port > 65535 {
		return fmt.Errorf("daemon: invalid port %d (want 1-65535)", *port)
	}
	addr := net.JoinHostPort(strings.TrimSpace(*bind), strconv.Itoa(*port))

	token, err := daemon.EnsureTokenAt(daemon.ResolveTokenFile(*root))
	if err != nil {
		return err
	}
	d, err := daemon.NewDaemon(*root, token)
	if err != nil {
		return err
	}
	d.ServerURL = strings.TrimSpace(*serverURL)
	d.ServerToken = strings.TrimSpace(*serverToken)
	d.UserID = strings.TrimSpace(*userID)

	// Background extraction pipeline (issue #115): Harvester + Watcher +
	// Processor with the interceptor sink wired at startup, designation
	// fail-closed unless the operator opts in, per-project LLM throttle
	// inside the processor provider (1 call/5min).
	proj := strings.TrimSpace(*project)
	if proj == "" {
		proj = filepath.Base(filepath.Clean(d.Root))
	}
	desig := daemon.NewStaticDesignation(autoDesignated(*serverToken))
	rt := daemon.NewRuntime(d, proj, desig)
	if rt != nil {
		d.SetPipeline(rt)
		rt.Materializer = &materializer.Materializer{
			ProjectID: proj,
			Targets:   materializer.DefaultTargets(),
			Debounce:  materializer.Debounce,
			Read: func(path string) (string, error) {
				data, err := daemon.ReadFile(d.Root, path)
				if err != nil {
					return "", err
				}
				return string(data), nil
			},
			Write: func(path, content string) error {
				return daemon.WriteFile(d.Root, path, []byte(content))
			},
		}
		go rt.Start(ctx)
		log.Printf("daemon: auto-sync harvester started (designated=%v, cursor JSONL → portal)", desig.IsDesignated())
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- d.Start(addr)
	}()

	var proxy *daemon.CORSProxy
	proxyErr := make(chan error, 1)
	if pa := strings.TrimSpace(*proxyAddr); pa != "" {
		proxy = daemon.NewCORSProxy(d)
		proxy.Addr = pa
		go func() {
			err := proxy.Start()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				proxyErr <- err
				return
			}
			proxyErr <- nil
		}()
		log.Printf("daemon: status UI + CORS proxy on http://%s/ (login & connection status)", pa)
	}

	if d.ServerURL != "" {
		if err := d.Register(ctx, d.ServerURL); err != nil {
			log.Printf("daemon: initial register failed: %v", err)
		} else if rt != nil {
			// Prefer server UUID over folder basename for portal writes.
			if pid, err := d.ResolveProjectIDForRoot(ctx); err == nil && pid != "" {
				rt.SetServerProjectID(pid)
			}
		}
		hbCtx, hbCancel := context.WithCancel(context.Background())
		defer hbCancel()
		go d.StartHeartbeat(hbCtx, d.ServerURL, daemon.HeartbeatInterval)
	} else {
		log.Print("daemon: no server set — local-only mode (no register/heartbeat)")
	}

	log.Printf("daemon: serving workspace root=%s addr=%s machine=%s server=%q project=%q",
		d.Root, addr, d.MachineID, d.ServerURL, proj)

	select {
	case <-ctx.Done():
		log.Print("daemon: shutdown signal received, draining")
	case err := <-serveErr:
		if proxy != nil {
			_ = proxy.Close()
		}
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case err := <-proxyErr:
		_ = d.Close()
		if err != nil {
			return fmt.Errorf("daemon: CORS proxy: %w", err)
		}
		return nil
	}
	if proxy != nil {
		if err := proxy.Close(); err != nil {
			log.Printf("daemon: CORS proxy close: %v", err)
		}
	}
	if err := d.Close(); err != nil {
		return err
	}
	if err := <-serveErr; err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
