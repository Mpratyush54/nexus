//go:build nexuscorelib

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"central-memory/internal/buildinfo"
	"central-memory/internal/config"
	"central-memory/internal/core"
	"central-memory/internal/diagnostics"
)

// Core-side diagnostics: the core's log goes to <base>/logs/core-*.log
// (beside the desktop's nexus-*.log), panics in nx_call and in the core's
// goroutines become held crash reports, and queued reports upload in the
// background with backoff.
var (
	diagOnce     sync.Once
	diagBase     string
	diagRing     = diagnostics.NewRing(0)
	diagUploader *diagnostics.Uploader
	diagWake     = make(chan struct{}, 1)
	diagDesktop  string
)

// setupDiagnostics runs once per process, on the first nx_init.
func setupDiagnostics(base, desktopVersion string) {
	diagOnce.Do(func() {
		base = strings.TrimSpace(base)
		if base == "" {
			base = diagnostics.DefaultDir()
		}
		diagBase = base
		diagDesktop = strings.TrimSpace(desktopVersion)
		version := buildinfo.Version
		header := func() string {
			return diagnostics.FormatLogLine(time.Now(), "INFO", diagnostics.SourceCore,
				fmt.Sprintf("log: Nexus core %s (commit %s), desktop %s, %s, pid %d",
					version, buildinfo.Commit, orUnknown(diagDesktop), diagnostics.OSString(), os.Getpid()))
		}
		file := diagnostics.NewRollingFile(diagnostics.LogsDir(base), "core", diagnostics.MaxLogFileBytes, diagnostics.MaxLogFiles, header)
		log.SetFlags(0)
		log.SetOutput(&diagnostics.LogWriter{Out: file, Ring: diagRing, Source: diagnostics.SourceCore})
		diagnostics.Configure(diagnostics.Config{Base: base, Version: version, Ring: diagRing})
		diagUploader = &diagnostics.Uploader{Base: base, Server: diagServerURL, Token: diagToken}
		diagnostics.Go("diagnostics-uploader", runDiagnosticsUploader)
		log.Printf("INFO nexuscore: started, logs in %s", diagnostics.LogsDir(base))
	})
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func diagServerURL() string {
	nxState.mu.Lock()
	defer nxState.mu.Unlock()
	if cloud, ok := nxState.deps.Cloud.(*core.HTTPCloud); ok && strings.TrimSpace(cloud.BaseURL) != "" {
		return cloud.BaseURL
	}
	if nxState.captureConfig != nil && nxState.captureConfig.serverURL != "" {
		return nxState.captureConfig.serverURL
	}
	return config.ResolveServerURL("https://api-nexus.pratyushes.dev")
}

func diagToken() string {
	nxState.mu.Lock()
	cloud, ok := nxState.deps.Cloud.(*core.HTTPCloud)
	nxState.mu.Unlock()
	if ok && strings.TrimSpace(cloud.Token) != "" {
		return cloud.Token
	}
	return embeddedServerToken("")
}

// runDiagnosticsUploader sends what was queued by an earlier run shortly
// after start, then checks once a minute (or when woken by a new report).
func runDiagnosticsUploader() {
	timer := time.NewTimer(15 * time.Second)
	for {
		select {
		case <-timer.C:
		case <-diagWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		for _, r := range diagUploader.Flush(context.Background(), "", false) {
			logUploadResult(r)
		}
		timer.Reset(time.Minute)
	}
}

func logUploadResult(r diagnostics.UploadResult) {
	switch {
	case r.Sent:
		log.Printf("INFO diagnostics: sent report %s ref=%s", r.ReportID, r.RequestID)
	case r.Dropped:
		log.Printf("WARN diagnostics: server rejected report %s (status %d ref=%s): %s", r.ReportID, r.Status, r.RequestID, r.Error)
	case r.Waiting && r.Error != "":
		log.Printf("WARN diagnostics: report %s not sent, retry at %s: %s", r.ReportID, r.RetryAt.Format(time.RFC3339), r.Error)
	}
}

// diagnosticsReportsCall serves the desktop's report methods:
//
//	diagnostics.reports.flush  {report_id?, force?} → {results:[…]}
//	diagnostics.reports.status → {pending, held, logs_dir, reports_dir}
func diagnosticsReportsCall(name string, args json.RawMessage) (any, error) {
	if diagUploader == nil {
		setupDiagnostics("", "")
	}
	switch name {
	case "diagnostics.reports.flush":
		var req struct {
			ReportID string `json:"report_id"`
			Force    bool   `json:"force"`
		}
		_ = json.Unmarshal(args, &req)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		results := diagUploader.Flush(ctx, strings.TrimSpace(req.ReportID), req.Force)
		for _, r := range results {
			logUploadResult(r)
		}
		if results == nil {
			results = []diagnostics.UploadResult{}
		}
		return map[string]any{"results": results}, nil
	case "diagnostics.reports.status":
		held, _ := os.ReadDir(diagnostics.HeldDir(diagBase))
		return map[string]any{
			"pending":     len(diagUploader.Pending()),
			"held":        len(held),
			"logs_dir":    diagnostics.LogsDir(diagBase),
			"reports_dir": diagnostics.ReportsDir(diagBase),
		}, nil
	case "diagnostics.reports.wake":
		select {
		case diagWake <- struct{}{}:
		default:
		}
		return map[string]any{"ok": true}, nil
	}
	return nil, fmt.Errorf("unknown method %s", name)
}
