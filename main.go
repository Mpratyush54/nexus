// Command mem — central memory multiplayer control plane.
//
// Phase 1 commands: daemon, status, projects.
// Phase 2+: memory, sessions, agents, branches.
package main

import (
	"fmt"
	"os"

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

Planned:
  mem daemon [--port PORT]         start workspace daemon
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
