//go:build nexuscorelib

package main

import (
	"os"
	"runtime/debug"
	"strings"
)

// The Go heap inside the desktop app has a soft memory limit so capture
// bursts are collected sooner instead of letting the heap double (GOGC).
// It is adaptive (memory_watch.go): never below minCoreMemoryLimit and kept
// at 1.5 times the live heap, so it never sits at or below what is really
// in use — a fixed 256 MiB limit under a 262 MiB live heap made the
// collector run about 300 times a minute. GOMEMLIMIT overrides all of it.
const minCoreMemoryLimit = 384 << 20

func memoryLimitFromEnv() bool {
	return strings.TrimSpace(os.Getenv("GOMEMLIMIT")) != ""
}

func init() {
	if memoryLimitFromEnv() {
		return
	}
	debug.SetMemoryLimit(minCoreMemoryLimit)
}
