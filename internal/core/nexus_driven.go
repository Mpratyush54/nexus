package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A chat run inside Nexus is saved under its Nexus session. Before capture
// learned to skip those agents' own transcripts, it also imported each one as
// a separate captured chat: the same conversation listed twice. Such a copy
// is recognisable exactly: its native agent session id appears in exchanges
// Nexus recorded under a different chat. The chat list leaves those copies
// out.

type drivenIndex struct {
	mu      sync.Mutex
	root    string
	builtAt time.Time
	gen     uint64
	drivers map[string]map[string]bool // native id -> Nexus session ids that drove it
}

var nexusDriven = &drivenIndex{}

// drivenGen changes whenever an exchange is saved, so the index rebuilds.
var drivenGen struct {
	sync.Mutex
	n uint64
}

func bumpDrivenIndex() {
	drivenGen.Lock()
	drivenGen.n++
	drivenGen.Unlock()
}

const drivenIndexTTL = time.Minute

func (d *drivenIndex) lookup(rootOverride string) map[string]map[string]bool {
	root, err := continuationRoot(rootOverride)
	if err != nil {
		return nil
	}
	drivenGen.Lock()
	gen := drivenGen.n
	drivenGen.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.drivers != nil && d.root == root && d.gen == gen && time.Since(d.builtAt) < drivenIndexTTL {
		return d.drivers
	}
	drivers := map[string]map[string]bool{}
	names, _ := filepath.Glob(filepath.Join(root, "*", "*.json"))
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		var ex struct {
			SessionID string `json:"session_id"`
			NativeID  string `json:"native_id"`
		}
		if json.Unmarshal(raw, &ex) != nil {
			continue
		}
		native := strings.TrimSpace(ex.NativeID)
		if native == "" || ex.SessionID == "" {
			continue
		}
		if drivers[native] == nil {
			drivers[native] = map[string]bool{}
		}
		drivers[native][ex.SessionID] = true
	}
	d.root, d.gen, d.builtAt, d.drivers = root, gen, time.Now(), drivers
	return drivers
}

// hideNexusDrivenCopies drops captured copies of chats Nexus already saves
// from a timeline.list result ({"items":[{session_id, native_id, ...}]}).
func hideNexusDrivenCopies(out any, rootOverride string) any {
	m, ok := out.(map[string]any)
	if !ok {
		return out
	}
	items, ok := m["items"].([]any)
	if !ok || len(items) == 0 {
		return out
	}
	drivers := nexusDriven.lookup(rootOverride)
	if len(drivers) == 0 {
		return out
	}
	kept := items[:0:0]
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		native, _ := item["native_id"].(string)
		session, _ := item["session_id"].(string)
		if by := drivers[strings.TrimSpace(native)]; len(by) > 0 && !by[session] {
			continue // the same conversation is listed under the Nexus chat
		}
		kept = append(kept, raw)
	}
	m["items"] = kept
	return m
}
