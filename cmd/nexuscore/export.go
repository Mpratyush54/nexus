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
	"sync"
	"unsafe"

	"central-memory/internal/core"
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
		Online bool `json:"online"`
	}
	if configJSON != nil {
		_ = json.Unmarshal([]byte(C.GoString(configJSON)), &cfg)
	}
	nxState.mu.Lock()
	nxState.deps.Online = cfg.Online
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
	nxState.mu.Lock()
	nxState.deps = core.Deps{}
	nxState.mu.Unlock()
	return 0
}
