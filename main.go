// Command mem — central memory multiplayer control plane.
//
// Phase 1 commands: daemon, status, projects.
// Phase 2+: memory, sessions, agents, branches.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/platform"
	"central-memory/internal/project"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "projects":
		err = cmdProjects(os.Args[2:])
	case "status":
		err = cmdStatus()
	case "daemon":
		err = cmdDaemon(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`mem — central memory (multiplayer rewrite)

  mem projects                     list detected projects
  mem status                       show system status
  mem daemon install               register auto-start-on-login service
  mem daemon uninstall             remove auto-start-on-login service
  mem daemon status                report daemon service state

Planned:
  mem memory search|write          query/write project memory
  mem sessions                     list active sessions`)
}

// cmdProjects — reuses internal/project identity resolution.
func cmdProjects(args []string) error {
	for _, leaf := range project.Leaves() {
		origin, root := project.Fingerprint(leaf)
		id := leaf
		if origin != "" {
			id += "  [" + origin + "]"
		} else if root != "" && len(root) >= 12 {
			id += "  [root " + root[:12] + "]"
		}
		fmt.Println(id)
	}
	_ = args
	return nil
}

// cmdStatus — minimal health check for the new architecture.
func cmdStatus() error {
	fmt.Println("mem status — multiplayer central memory")
	fmt.Println()
	leaves := project.CachedLeaves()
	fmt.Printf("projects detected: %d\n", len(leaves))
	fmt.Println("daemon: not yet implemented")
	fmt.Println("server: not yet implemented")
	return nil
}

// cmdDaemon — dispatches `daemon install|uninstall|status` to the
// internal/platform hooks (Issue #43). Follows the cmdProjects/cmdStatus
// style: thin CLI wrapper, real work lives in internal/platform/.
func cmdDaemon(args []string) error {
	if len(args) < 1 {
		cmdDaemonUsage()
		return fmt.Errorf("daemon requires one of install|uninstall|status")
	}
	switch args[0] {
	case "install":
		bin := strings.TrimSpace(os.Getenv("NEXUS_DAEMON_BIN"))
		if bin == "" {
			return fmt.Errorf("NEXUS_DAEMON_BIN must point to the daemon executable")
		}
		bin, err := filepath.Abs(bin)
		if err != nil {
			return fmt.Errorf("resolve daemon executable: %w", err)
		}
		return platform.Install(bin, nil)
	case "uninstall":
		return platform.Uninstall()
	case "status":
		st, err := platform.Status()
		if err != nil {
			return err
		}
		fmt.Println("daemon service:", st)
		return nil
	default:
		cmdDaemonUsage()
		return fmt.Errorf("unknown daemon subcommand %q", args[0])
	}
}

// cmdDaemonUsage — prints daemon subcommand help.
func cmdDaemonUsage() {
	fmt.Println(`mem daemon — manage the workspace daemon service

  mem daemon install               register auto-start-on-login service
  mem daemon uninstall             remove auto-start-on-login service
  mem daemon status                report daemon service state`)
}
