//go:build nexuscorelib

package main

/*
#include <stdlib.h>
typedef void (*nx_event_fn)(const char *json);
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"central-memory/internal/config"
	"central-memory/internal/continuex"
	"central-memory/internal/core"
	"central-memory/internal/daemon"
)

type coreState struct {
	mu                sync.Mutex
	deps              core.Deps
	captureCancel     context.CancelFunc
	captureDone       chan struct{}
	captureRuntime    *daemon.Runtime
	captureOthers     map[string]*daemon.Runtime
	captureWorkErrors map[string]string
	captureConfig     *embeddedCaptureConfig
	capturePaused     bool
	captureStarting   bool
	captureGeneration uint64
	captureError      string
	captureProjectID  string
}

var nxState = coreState{}
var nxEventFn C.nx_event_fn

type embeddedCaptureConfig struct {
	generation  uint64
	root        string
	serverURL   string
	serverToken string
	userID      string
	projectID   string
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
	defer func() {
		if recover() != nil {
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
	}
	if configJSON != nil {
		_ = json.Unmarshal([]byte(C.GoString(configJSON)), &cfg)
	}
	stopEmbeddedCapture()

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
	deps := core.Deps{Root: root, Online: cfg.Online && serverToken != "", Capture: embeddedCaptureController{}, ContinueExecutor: continuex.HeadlessExecutor{}}
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
	nxState.captureGeneration++
	if root == "" {
		nxState.captureConfig = nil
	} else {
		nxState.captureConfig = &embeddedCaptureConfig{
			generation: nxState.captureGeneration,
			root:       root, serverURL: serverURL, serverToken: serverToken,
			userID: cfg.UserID, projectID: cfg.ProjectID,
		}
	}
	nxState.mu.Unlock()
	if root != "" {
		startConfiguredEmbeddedCapture()
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
		var others sync.WaitGroup
		for _, extra := range captureRoots(root)[1:] {
			others.Add(1)
			go func(extraRoot string) {
				defer others.Done()
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
				go d.StartHeartbeat(ctx, d.ServerURL, daemon.HeartbeatInterval)
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
	nxState.mu.Unlock()
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
	defer func() {
		if recover() != nil {
			out = C.CString(`{"ok":false,"error":"core panic"}`)
		}
	}()
	name := ""
	if method != nil {
		name = C.GoString(method)
	}
	var args json.RawMessage
	if request != nil {
		args = json.RawMessage(C.GoString(request))
	}
	nxState.mu.Lock()
	deps := nxState.deps
	nxState.mu.Unlock()
	result, err := core.Call(context.Background(), name, args, deps)
	return C.CString(string(core.MarshalResult(result, err)))
}

//export nx_subscribe
func nx_subscribe(fn C.nx_event_fn) C.int {
	// Stored for a Go thread to invoke later. The desktop copies the
	// payload and posts it to its UI queue. It must not call nx_call here.
	nxEventFn = fn
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
	stopEmbeddedCapture()
	nxState.mu.Lock()
	nxState.deps = core.Deps{}
	nxState.captureConfig = nil
	nxState.capturePaused = false
	nxState.captureStarting = false
	nxState.mu.Unlock()
	return 0
}
