//go:build nexuscorelib

package main

/*
#include <stdlib.h>
typedef void (*nx_event_fn)(const char *json);
static void nx_emit(nx_event_fn fn, const char *json) { if (fn) fn(json); }
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unsafe"

	"central-memory/internal/config"
	"central-memory/internal/continuex"
	"central-memory/internal/core"
	"central-memory/internal/daemon"
	"central-memory/internal/diagnostics"
	"central-memory/internal/platform"
)

type coreState struct {
	mu                  sync.Mutex
	deps                core.Deps
	captureCancel       context.CancelFunc
	captureDone         chan struct{}
	captureRuntime      *daemon.Runtime
	captureOthers       map[string]*daemon.Runtime
	captureWorkErrors   map[string]string
	captureWorkProjects map[string]string
	captureConfig       *embeddedCaptureConfig
	capturePaused       bool
	captureStarting     bool
	captureGeneration   uint64
	captureError        string
	captureProjectID    string
}

var nxState = coreState{}
var nxEventFn C.nx_event_fn
var nxEventMu sync.RWMutex
var captureConfigureMu sync.Mutex
var continuationCancels sync.Map // request id -> context.CancelFunc

type embeddedCaptureConfig struct {
	generation  uint64
	root        string
	serverURL   string
	serverToken string
	userID      string
	projectID   string
	roots       []string
}

type embeddedCaptureController struct{}

// The desktop's saved sign-in is authoritative for this process. A stale
// NEXUS_TOKEN in the parent environment must not silently sign it out.
func embeddedServerToken(explicit string) string {
	if token := strings.TrimSpace(explicit); token != "" {
		return token
	}
	if saved, err := config.LoadFile(); err == nil {
		if token := strings.TrimSpace(saved.Token); token != "" {
			return token
		}
	}
	return config.ResolveToken()
}

//export nx_init
func nx_init(configJSON *C.char) (rc C.int) {
	captureConfigureMu.Lock()
	defer captureConfigureMu.Unlock()
	defer func() {
		if v := recover(); v != nil {
			diagnostics.RecordPanic("nx_init", v, debug.Stack())
			rc = 1
		}
	}()
	var cfg struct {
		Online        bool   `json:"online"`
		WorkspaceRoot string `json:"workspace_root"`
		ServerURL     string `json:"server_url"`
		ServerToken   string `json:"server_token"`
		UserID        string `json:"user_id"`
		ProjectID     string `json:"project_id"`
		// Where logs/ and reports/ live (the desktop's profile-aware
		// LOCALAPPDATA Nexus folder) and the desktop's version for log headers.
		DiagnosticsDir string `json:"diagnostics_dir"`
		AppVersion     string `json:"app_version"`
	}
	if configJSON != nil {
		_ = json.Unmarshal([]byte(C.GoString(configJSON)), &cfg)
	}
	setupDiagnostics(cfg.DiagnosticsDir, cfg.AppVersion)
	// A Finder-launched macOS app has launchd's bare PATH; without this
	// every agent CLI looks uninstalled. No-op on other systems.
	platform.AugmentGUIPath()
	// Go raises the open-file limit only for executables; inside the
	// desktop app the library inherits launchd's soft limit of 256.
	platform.RaiseFileLimit()
	stopEmbeddedCapture()
	stopDesktopExecution()

	serverURL := strings.TrimSpace(cfg.ServerURL)
	if serverURL == "" {
		serverURL = config.ResolveServerURL("https://api-nexus.pratyushes.dev")
	}
	serverToken := embeddedServerToken(cfg.ServerToken)
	root := embeddedWorkspaceRoot(cfg.WorkspaceRoot)
	if strings.TrimSpace(cfg.WorkspaceRoot) != "" && root != "" {
		if err := config.SaveFile(config.File{WorkspaceRoot: root}); err != nil {
			log.Printf("nexuscore: save capture workspace: %v", err)
		}
	}
	// Only an explicit in-app Send invokes this bounded, console-free executor.
	// continue.start remains command-preview only and never launches a process.
	deps := core.Deps{Root: root, Online: cfg.Online && serverToken != "", Capture: embeddedCaptureController{}, ContinueExecutor: continuex.HeadlessExecutor{}}
	roots := captureRoots(root)
	if deps.Online {
		deps.Cloud = &core.HTTPCloud{BaseURL: serverURL, Token: serverToken}
	}
	nxState.mu.Lock()
	nxState.deps = deps
	nxState.capturePaused = false
	nxState.captureError = ""
	nxState.captureProjectID = ""
	nxState.captureOthers = make(map[string]*daemon.Runtime)
	nxState.captureWorkErrors = make(map[string]string)
	nxState.captureWorkProjects = make(map[string]string)
	nxState.captureGeneration++
	if len(roots) == 0 {
		nxState.captureConfig = nil
	} else {
		nxState.captureConfig = &embeddedCaptureConfig{
			generation: nxState.captureGeneration,
			root:       roots[0], roots: roots, serverURL: serverURL, serverToken: serverToken,
			userID: cfg.UserID, projectID: cfg.ProjectID,
		}
	}
	nxState.mu.Unlock()
	if len(roots) > 0 {
		startConfiguredEmbeddedCapture()
	}
	// A subscribed UI must be available before asking for local approval.
	nxEventMu.RLock()
	subscribed := nxEventFn != nil
	nxEventMu.RUnlock()
	if subscribed {
		startDesktopExecution()
	}
	return 0
}

// embeddedWorkspaceRoot refuses broad guesses such as a user's home or the
// install directory. The desktop passes a git workspace explicitly; CLI and
// advanced users can provide NEXUS_WORKSPACE during the migration period.
func embeddedWorkspaceRoot(configured string) string {
	root := strings.TrimSpace(configured)
	if root == "" {
		if saved, err := config.LoadFile(); err == nil {
			root = strings.TrimSpace(saved.WorkspaceRoot)
		}
	}
	if root == "" {
		root = strings.TrimSpace(os.Getenv("NEXUS_WORKSPACE"))
	}
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && samePath(abs, home) {
		return ""
	}
	return filepath.Clean(abs)
}

func samePath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}

// startEmbeddedCapture runs the existing mature harvester pipeline inside
// nexuscore.dll. It deliberately does not call Daemon.Start, so the native
// app owns capture without starting the retired localhost control server.
func startConfiguredEmbeddedCapture() {
	nxState.mu.Lock()
	if nxState.capturePaused || nxState.captureConfig == nil || nxState.captureCancel != nil || nxState.captureStarting {
		nxState.mu.Unlock()
		return
	}
	cfg := *nxState.captureConfig
	nxState.captureStarting = true
	nxState.mu.Unlock()
	startEmbeddedCapture(cfg)
}

func startEmbeddedCapture(cfg embeddedCaptureConfig) {
	root := cfg.root
	serverURL := cfg.serverURL
	serverToken := cfg.serverToken
	userID := cfg.userID
	projectID := cfg.projectID
	localToken, err := daemon.GenerateToken()
	if err != nil {
		clearCaptureStarting()
		log.Printf("nexuscore: create embedded capture token: %v", err)
		return
	}
	d, err := daemon.NewDaemon(root, localToken)
	if err != nil {
		clearCaptureStarting()
		log.Printf("nexuscore: start embedded capture: %v", err)
		return
	}
	d.ServerURL = strings.TrimSpace(serverURL)
	d.ServerToken = strings.TrimSpace(serverToken)
	d.UserID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		projectID = filepath.Base(root)
	}
	runtime := daemon.NewRuntime(d, projectID, daemon.NewStaticDesignation(d.ServerToken != ""))
	if runtime == nil {
		clearCaptureStarting()
		return
	}
	// All workspaces share one machine-wide transcript reader.
	runtime.Shared = daemon.DefaultSharedScanner()
	d.SetPipeline(runtime)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	nxState.mu.Lock()
	if nxState.capturePaused || nxState.captureConfig == nil || nxState.captureConfig.generation != cfg.generation {
		nxState.captureStarting = false
		nxState.mu.Unlock()
		cancel()
		return
	}
	nxState.captureCancel, nxState.captureDone, nxState.captureRuntime = cancel, done, runtime
	nxState.captureStarting = false
	if serverToken == "" {
		nxState.captureError = "Sign in to upload captured sessions to Timeline"
	}
	nxState.mu.Unlock()
	go func() {
		defer close(done)
		defer diagnostics.Guard("capture")()
		var others sync.WaitGroup
		for index, extra := range cfg.roots {
			if index == 0 {
				continue
			}
			others.Add(1)
			go func(extraRoot string) {
				defer others.Done()
				defer diagnostics.Guard("capture " + filepath.Base(extraRoot))()
				runAdditionalCapture(ctx, cfg, extraRoot)
			}(extra)
		}
		runCaptureRuntime(ctx, cfg, root, d, runtime)
		others.Wait()
	}()
}

func runAdditionalCapture(ctx context.Context, cfg embeddedCaptureConfig, root string) {
	localToken, err := daemon.GenerateToken()
	if err != nil {
		setCaptureConnection(cfg.generation, root, err.Error(), "")
		return
	}
	d, err := daemon.NewDaemon(root, localToken)
	if err != nil {
		setCaptureConnection(cfg.generation, root, err.Error(), "")
		return
	}
	d.ServerURL, d.ServerToken, d.UserID = cfg.serverURL, cfg.serverToken, cfg.userID
	runtime := daemon.NewRuntime(d, filepath.Base(root), daemon.NewStaticDesignation(d.ServerToken != ""))
	if runtime == nil {
		return
	}
	runtime.Shared = daemon.DefaultSharedScanner()
	d.SetPipeline(runtime)
	nxState.mu.Lock()
	if nxState.captureConfig == nil || nxState.captureConfig.generation != cfg.generation || ctx.Err() != nil {
		nxState.mu.Unlock()
		return
	}
	runtime.SetPaused(nxState.capturePaused)
	nxState.captureOthers[root] = runtime
	nxState.mu.Unlock()
	runCaptureRuntime(ctx, cfg, root, d, runtime)
}

func runCaptureRuntime(ctx context.Context, cfg embeddedCaptureConfig, root string, d *daemon.Daemon, runtime *daemon.Runtime) {
	if d.ServerURL != "" && d.ServerToken != "" {
		for ctx.Err() == nil {
			if err := d.Register(ctx, d.ServerURL); err != nil {
				setCaptureConnection(cfg.generation, root, "Registration failed: "+err.Error(), "")
			} else if id, err := d.ResolveProjectIDForRoot(ctx); err != nil {
				setCaptureConnection(cfg.generation, root, "Project resolution failed: "+err.Error(), "")
			} else {
				runtime.SetServerProjectID(id)
				setCaptureConnection(cfg.generation, root, "", id)
				diagnostics.Go("heartbeat", func() { d.StartHeartbeat(ctx, d.ServerURL, daemon.HeartbeatInterval) })
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}
	if ctx.Err() == nil {
		runtime.Start(ctx)
	}
}

func setCaptureConnection(generation uint64, root, problem, projectID string) {
	nxState.mu.Lock()
	defer nxState.mu.Unlock()
	if nxState.captureConfig != nil && nxState.captureConfig.generation == generation {
		if problem == "" {
			delete(nxState.captureWorkErrors, root)
			nxState.captureWorkProjects[root] = projectID
		} else {
			nxState.captureWorkErrors[root] = problem
		}
		if samePath(root, nxState.captureConfig.root) {
			nxState.captureError = problem
			nxState.captureProjectID = projectID
		}
	}
}

func clearCaptureStarting() {
	nxState.mu.Lock()
	nxState.captureStarting = false
	nxState.mu.Unlock()
}

func (embeddedCaptureController) Pause() error {
	nxState.mu.Lock()
	if nxState.captureConfig == nil {
		nxState.mu.Unlock()
		return fmt.Errorf("select a workspace before starting capture")
	}
	nxState.capturePaused = true
	runtime := nxState.captureRuntime
	others := make([]*daemon.Runtime, 0, len(nxState.captureOthers))
	for _, r := range nxState.captureOthers {
		others = append(others, r)
	}
	nxState.mu.Unlock()
	if runtime != nil {
		runtime.SetPaused(true)
	}
	for _, r := range others {
		r.SetPaused(true)
	}
	return nil
}

func (embeddedCaptureController) Resume() error {
	nxState.mu.Lock()
	if nxState.captureConfig == nil {
		nxState.mu.Unlock()
		return fmt.Errorf("select a workspace before starting capture")
	}
	nxState.capturePaused = false
	runtime := nxState.captureRuntime
	others := make([]*daemon.Runtime, 0, len(nxState.captureOthers))
	for _, r := range nxState.captureOthers {
		others = append(others, r)
	}
	nxState.mu.Unlock()
	if runtime != nil {
		runtime.SetPaused(false)
		for _, r := range others {
			r.SetPaused(false)
		}
		return nil
	}
	startConfiguredEmbeddedCapture()
	return nil
}

func (embeddedCaptureController) Status() any {
	nxState.mu.Lock()
	configured := nxState.captureConfig != nil
	runtime := nxState.captureRuntime
	others := make([]*daemon.Runtime, 0, len(nxState.captureOthers))
	for _, r := range nxState.captureOthers {
		others = append(others, r)
	}
	errorsByWorkspace := make(map[string]string, len(nxState.captureWorkErrors))
	for root, problem := range nxState.captureWorkErrors {
		errorsByWorkspace[root] = problem
	}
	status := map[string]any{
		"configured":          configured,
		"paused":              nxState.capturePaused,
		"running":             configured && runtime != nil && nxState.captureCancel != nil,
		"error":               nxState.captureError,
		"project_id":          nxState.captureProjectID,
		"matched_files":       0,
		"new_turns_last_scan": 0,
		"workspaces": func() int {
			if !configured {
				return 0
			}
			return 1 + len(others)
		}(),
		"workspace_errors": errorsByWorkspace,
	}
	runtimes := make(map[string]*daemon.Runtime, len(nxState.captureOthers)+1)
	if nxState.captureConfig != nil {
		runtimes[nxState.captureConfig.root] = runtime
	}
	for root, r := range nxState.captureOthers {
		runtimes[root] = r
	}
	projects := make(map[string]string, len(nxState.captureWorkProjects))
	for root, id := range nxState.captureWorkProjects {
		projects[root] = id
	}
	nxState.mu.Unlock()
	saved, _ := config.LoadFile()
	status["consent_required"] = saved.CaptureConsentVersion < 1
	status["approved_workspaces"] = saved.CaptureWorkspaces
	suggestions := []string{}
	for _, path := range append([]string{saved.WorkspaceRoot}, saved.RecentWorkspaces...) {
		if path != "" {
			suggestions = append(suggestions, path)
		}
	}
	// Folders where agents have actually run, found from transcript headers
	// (cached; refreshed in the background at most every 10 minutes).
	discovered := daemon.CachedDiscoveredProjects()
	approvedSet := map[string]bool{}
	for _, path := range saved.CaptureWorkspaces {
		approvedSet[strings.ToLower(filepath.Clean(path))] = true
	}
	discoveredRows := make([]map[string]any, 0, len(discovered))
	for _, project := range discovered {
		key := strings.ToLower(filepath.Clean(project.Path))
		discoveredRows = append(discoveredRows, map[string]any{
			"path": project.Path, "name": project.Name, "sessions": project.Sessions,
			"agents": project.Agents, "last_active": project.LastActive, "approved": approvedSet[key],
		})
		if len(suggestions) < 40 {
			suggestions = append(suggestions, project.Path)
		}
	}
	status["discovered_workspaces"] = discoveredRows
	status["suggested_workspaces"] = suggestions
	details := make([]map[string]any, 0, len(saved.CaptureWorkspaces))
	for _, root := range saved.CaptureWorkspaces {
		row := map[string]any{"path": root, "project_id": projects[root], "state": "connecting"}
		if _, err := os.Stat(root); err != nil {
			row["state"] = "unavailable"
		}
		if problem := errorsByWorkspace[root]; problem != "" {
			row["state"], row["error"] = "needs attention", problem
		}
		if rt := runtimes[root]; rt != nil && rt.Harvester != nil {
			stats := rt.Harvester.Stats()
			row["state"] = "monitoring"
			if problem := errorsByWorkspace[root]; problem != "" {
				row["state"] = "needs attention"
			}
			row["last_scan_at"], row["matched_files"], row["agent_files"] = stats.LastScanAt, stats.LastScanFiles, stats.AgentFiles
		}
		if status["paused"] == true {
			row["state"] = "paused"
		}
		details = append(details, row)
	}
	status["workspace_details"] = details
	// Owned transcripts that capture does not read (size caps), never silent.
	status["skipped_files"] = daemon.SkippedTranscripts()
	sources := []map[string]string{}
	seenSources := map[string]bool{}
	for _, source := range daemon.ResolveSources() {
		for _, path := range source.Dirs {
			key := source.Agent + "|" + strings.ToLower(path)
			if info, err := os.Stat(path); err == nil && info.IsDir() && !seenSources[key] {
				sources = append(sources, map[string]string{"agent": source.Agent, "path": path})
				seenSources[key] = true
			}
		}
	}
	status["sources"] = sources
	if runtime != nil && runtime.Harvester != nil {
		stats := runtime.Harvester.Stats()
		status["last_scan_at"] = stats.LastScanAt
		status["matched_files"] = stats.LastScanFiles
		status["new_turns_last_scan"] = stats.LastScanTurns
		status["last_scan_error"] = stats.LastScanError
		status["agent_files"] = stats.AgentFiles
	}
	for _, other := range others {
		if other != nil && other.Harvester != nil {
			stats := other.Harvester.Stats()
			status["matched_files"] = status["matched_files"].(int) + stats.LastScanFiles
			status["new_turns_last_scan"] = status["new_turns_last_scan"].(int) + stats.LastScanTurns
		}
	}
	return status
}

func (embeddedCaptureController) ConfigureWorkspaces(request []string) (any, error) {
	captureConfigureMu.Lock()
	defer captureConfigureMu.Unlock()
	roots, err := validateCaptureRoots(request, true)
	if err != nil {
		return nil, err
	}
	if err := config.SetCaptureWorkspaces(roots); err != nil {
		return nil, err
	}
	// Invalidate startup work before cancelling existing runtimes, so a late
	// registration cannot restart a workspace after its consent was removed.
	nxState.mu.Lock()
	var previous embeddedCaptureConfig
	if nxState.captureConfig != nil {
		previous = *nxState.captureConfig
	}
	if cloud, ok := nxState.deps.Cloud.(*core.HTTPCloud); ok {
		previous.serverURL, previous.serverToken = cloud.BaseURL, cloud.Token
	}
	nxState.captureConfig = nil
	nxState.captureGeneration++
	nxState.mu.Unlock()
	stopEmbeddedCapture()
	if previous.serverURL == "" {
		previous.serverURL = config.ResolveServerURL("https://api-nexus.pratyushes.dev")
	}
	if previous.serverToken == "" {
		previous.serverToken = embeddedServerToken("")
	}
	if previous.userID == "" {
		previous.userID = config.ResolveUserID()
	}
	nxState.mu.Lock()
	nxState.captureOthers = make(map[string]*daemon.Runtime)
	nxState.captureWorkErrors = make(map[string]string)
	nxState.captureWorkProjects = make(map[string]string)
	nxState.captureStarting, nxState.capturePaused = false, false
	nxState.captureError, nxState.captureProjectID = "", ""
	if len(roots) > 0 {
		previous.generation, previous.root, previous.roots, previous.projectID = nxState.captureGeneration, roots[0], roots, ""
		nxState.captureConfig = &previous
		nxState.deps.Root = roots[0]
	}
	nxState.mu.Unlock()
	if len(roots) > 0 {
		startConfiguredEmbeddedCapture()
	}
	return (embeddedCaptureController{}).Status(), nil
}

func stopEmbeddedCapture() {
	nxState.mu.Lock()
	cancel, done := nxState.captureCancel, nxState.captureDone
	nxState.captureCancel, nxState.captureDone, nxState.captureRuntime = nil, nil, nil
	nxState.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			log.Print("nexuscore: embedded capture did not stop within 5s")
		}
	}
}

//export nx_call
func nx_call(method, request *C.char) (out *C.char) {
	name := ""
	defer func() {
		if v := recover(); v != nil {
			// The app stays up; the panic is logged with its stack and held
			// as a crash report the desktop offers to send.
			id := diagnostics.RecordPanic("nx_call "+name, v, debug.Stack())
			msg := "Nexus hit an internal error"
			if id != "" {
				msg += " (report " + id + ")"
			}
			out = C.CString(string(core.MarshalResult(nil, errors.New(msg))))
		}
	}()
	if method != nil {
		name = C.GoString(method)
	}
	var args json.RawMessage
	if request != nil {
		args = json.RawMessage(C.GoString(request))
	}
	if strings.HasPrefix(name, "diagnostics.reports.") {
		result, err := diagnosticsReportsCall(name, args)
		return C.CString(string(core.MarshalResult(result, err)))
	}
	if strings.HasPrefix(name, "desktop.execution.") {
		result, err := desktopExecutionCall(name, args)
		return C.CString(string(core.MarshalResult(result, err)))
	}
	if strings.HasPrefix(name, "live.") {
		result, err := liveCall(name, args)
		return C.CString(string(core.MarshalResult(result, err)))
	}
	if strings.HasPrefix(name, "agents.memory_files.") {
		result, err := agentMemoryFilesCall(context.Background(), name, args)
		return C.CString(string(core.MarshalResult(result, err)))
	}
	if name == "agents.install_all" || name == "agents.uninstall_all" {
		result, err := agentsInstallCall(context.Background(), name, args)
		return C.CString(string(core.MarshalResult(result, err)))
	}
	if name == "agents.setup.browse" || name == "agents.setup.read" || name == "agents.setup.mcp_remove" {
		result, err := agentsBrowseCall(name, args)
		return C.CString(string(core.MarshalResult(result, err)))
	}
	nxState.mu.Lock()
	deps := nxState.deps
	nxState.mu.Unlock()
	deps.ContinueCancel = func(requestID string) bool {
		value, found := continuationCancels.Load(requestID)
		if found {
			value.(context.CancelFunc)()
		}
		return found
	}
	callCtx := context.Background()
	if name == "continue.here" {
		var envelope struct {
			RequestID string `json:"request_id"`
		}
		_ = json.Unmarshal(args, &envelope)
		if envelope.RequestID != "" {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithCancel(callCtx)
			if _, loaded := continuationCancels.LoadOrStore(envelope.RequestID, cancel); loaded {
				cancel()
				return C.CString(string(core.MarshalResult(nil, fmt.Errorf("request is already in progress"))))
			}
			defer func() { continuationCancels.Delete(envelope.RequestID); cancel() }()
		}
		deps.ContinueProgress = func(requestID string, update continuex.StreamProgress) {
			if requestID == "" {
				return
			}
			emitCoreEvent(map[string]string{"type": "continue.progress", "request_id": requestID, "kind": update.Kind, "text": update.Text})
		}
	}
	result, err := core.Call(callCtx, name, args, deps)
	return C.CString(string(core.MarshalResult(result, err)))
}

//export nx_subscribe
func nx_subscribe(fn C.nx_event_fn) C.int {
	// Stored for a Go thread to invoke later. The desktop copies the
	// payload and posts it to its UI queue. It must not call nx_call here.
	nxEventMu.Lock()
	nxEventFn = fn
	nxEventMu.Unlock()
	startDesktopExecution()
	return 0
}

//export nx_free
func nx_free(ptr *C.char) {
	if ptr != nil {
		C.free(unsafe.Pointer(ptr))
	}
}

//export nx_shutdown
func nx_shutdown() C.int {
	stopDesktopExecution()
	closeAllLive()
	continuationCancels.Range(func(_, value any) bool { value.(context.CancelFunc)(); return true })
	stopEmbeddedCapture()
	nxState.mu.Lock()
	nxState.deps = core.Deps{}
	nxState.captureConfig = nil
	nxState.capturePaused = false
	nxState.captureStarting = false
	nxState.mu.Unlock()
	return 0
}

func emitCoreEvent(value any) {
	nxEventMu.RLock()
	fn := nxEventFn
	nxEventMu.RUnlock()
	if fn == nil {
		return
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	message := C.CString(string(payload))
	C.nx_emit(fn, message)
	C.free(unsafe.Pointer(message))
}
