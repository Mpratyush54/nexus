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
	"strings"
	"sync"
	"unsafe"

	"central-memory/internal/cache"
	"central-memory/internal/cloudclient"
	"central-memory/internal/config"
	"central-memory/internal/core"
	"central-memory/internal/outbox"
	"central-memory/internal/platform"
)

type coreState struct {
	mu   sync.Mutex
	deps core.Deps
}

var nxState = coreState{}
var nxEventFn C.nx_event_fn

//export nx_init
func nx_init(configJSON *C.char) (rc C.int) {
	defer func() {
		if recover() != nil {
			rc = 1
		}
	}()
	var cfg struct {
		Online    bool   `json:"online"`
		ServerURL string `json:"server_url"`
		Token     string `json:"token"`
		Root      string `json:"workspace_root"`
	}
	cfg.Online = true
	if configJSON != nil {
		_ = json.Unmarshal([]byte(C.GoString(configJSON)), &cfg)
	}
	file, _ := config.LoadFile()
	if strings.TrimSpace(cfg.ServerURL) == "" {
		cfg.ServerURL = file.ServerURL
	}
	if strings.TrimSpace(cfg.Token) == "" {
		cfg.Token = file.Token
	}
	if strings.TrimSpace(cfg.Root) == "" {
		cfg.Root = file.WorkspaceRoot
	}

	var cloud core.Cloud
	client := cloudclient.New(cfg.ServerURL, cfg.Token)
	if client.SignedIn() {
		cloud = client
	}

	var c cache.Cache
	spool, _ := outbox.Open("")
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		if dir, err := platform.ConfigDir(); err == nil {
			root = dir
		}
	}

	nxState.mu.Lock()
	nxState.deps = core.Deps{
		Cloud:  cloud,
		Cache:  &c,
		Outbox: spool,
		Root:   root,
		Online: cfg.Online,
	}
	nxState.mu.Unlock()
	return 0
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

	// Hot-reload token if the user signed in after init.
	if deps.Cloud == nil {
		if tok := config.ResolveToken(); tok != "" {
			client := cloudclient.New("", tok)
			if client.SignedIn() {
				deps.Cloud = client
				nxState.mu.Lock()
				nxState.deps.Cloud = client
				nxState.mu.Unlock()
			}
		}
	}

	result, err := core.Call(context.Background(), name, args, deps)
	if name == "auth.login" && err == nil {
		client := cloudclient.New("", "")
		if client.SignedIn() {
			nxState.mu.Lock()
			nxState.deps.Cloud = client
			nxState.mu.Unlock()
		}
	}
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
	nxState.mu.Lock()
	nxState.deps = core.Deps{}
	nxState.mu.Unlock()
	return 0
}
