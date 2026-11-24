//go:build nexuscorelib

package main

import (
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof on http.DefaultServeMux
	"os"
	"strings"
)

// Opt-in profiling for the embedded core. The desktop app hosts the Go
// runtime in-process, where .NET tools see only "unmanaged" time; setting
// NEXUS_PPROF=127.0.0.1:6060 (loopback only) before launching Nexus serves
// Go's pprof endpoints, e.g.
//
//	go tool pprof -top http://127.0.0.1:6060/debug/pprof/profile?seconds=15
//	go tool pprof -top http://127.0.0.1:6060/debug/pprof/allocs
//
// Off by default; non-loopback addresses are refused.
func init() {
	addr := strings.TrimSpace(os.Getenv("NEXUS_PPROF"))
	if addr == "" {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !(host == "127.0.0.1" || host == "localhost" || host == "::1") {
		log.Printf("nexuscore: NEXUS_PPROF=%q ignored (use a loopback host:port)", addr)
		return
	}
	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Printf("nexuscore: pprof server: %v", err)
		}
	}()
}
