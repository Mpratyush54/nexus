//go:build nexuscorelib

package main

import (
	"os"
	"runtime/debug"
	"strings"
)

// coreMemoryLimit is a soft limit for the Go heap inside the desktop app.
// Capture and sync work allocates in bursts; with only GOGC the heap was
// allowed to double before collecting, so a long-running Nexus swung by
// hundreds of megabytes. Near this limit the collector runs sooner instead.
// It is soft: real need above it is still met. GOMEMLIMIT overrides it.
const coreMemoryLimit = 256 << 20

func init() {
	if strings.TrimSpace(os.Getenv("GOMEMLIMIT")) != "" {
		return
	}
	debug.SetMemoryLimit(coreMemoryLimit)
}
