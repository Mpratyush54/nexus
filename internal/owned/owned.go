// Package owned remembers which native agent sessions Nexus itself runs.
//
// Chats that run inside Nexus are saved by Nexus itself (continue.record).
// The agent still writes its own transcript under ~/.claude, ~/.codex, ...,
// and capture would import that as a second copy of the same chat. Nexus
// therefore remembers the native session ids of the agents it starts, and
// capture skips their transcripts. It is a leaf package so both the capture
// daemon and the desktop core can use it.
package owned

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const maxOwnedSessions = 5000

type registry struct {
	mu     sync.Mutex
	loaded bool
	ids    map[string]time.Time // lower-case native id -> when registered
	short  map[string]bool      // first 8 chars of UUID ids (Gemini file names)
	path   string
	// version counts in-memory changes; writeMu serialises persistence and
	// written records the newest version on disk, so an older snapshot is
	// never renamed over a newer one.
	version uint64
	writeMu sync.Mutex
	written uint64
}

var nexusOwned = &registry{}

// StatePath is the registry file. It is a var so tests can isolate it.
var StatePath = func() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "nexus", "nexus-owned-sessions.json")
}

var uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// geminiShortID matches the id suffix of Gemini chat files:
// session-2026-11-22T07-59-5d727dae.jsonl.
var geminiShortID = regexp.MustCompile(`^session-.*-([0-9a-fA-F]{8})\.json(l)?$`)

func (r *registry) loadLocked() {
	if r.loaded {
		return
	}
	r.loaded = true
	r.ids = map[string]time.Time{}
	r.short = map[string]bool{}
	r.path = StatePath()
	if r.path == "" {
		return
	}
	raw, err := os.ReadFile(r.path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		// Unreadable but present: never overwrite it with a near-empty
		// registry from this process.
		log.Printf("owned sessions: read %s: %v (not persisting this run)", r.path, err)
		r.path = ""
		return
	}
	var saved map[string]time.Time
	if err := json.Unmarshal(raw, &saved); err != nil {
		// Keep the damaged file aside instead of silently replacing it.
		aside := r.path + ".corrupt"
		if rerr := os.Rename(r.path, aside); rerr != nil {
			log.Printf("owned sessions: %s is corrupt (%v) and could not be moved aside: %v (not persisting this run)", r.path, err, rerr)
			r.path = ""
			return
		}
		log.Printf("owned sessions: %s is corrupt (%v); kept as %s", r.path, err, aside)
		return
	}
	for id, at := range saved {
		r.addLocked(id, at)
	}
}

func (r *registry) addLocked(id string, at time.Time) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return
	}
	r.ids[id] = at
	// Gemini names files by the first 8 hex of the UUID inside the id
	// (Kimi ids are "session_<uuid>").
	if u := uuidPattern.FindString(id); u != "" {
		r.short[u[:8]] = true
	}
}

// Mark records that Nexus started (and saves) this agent session.
func Mark(nativeID string) {
	nativeID = strings.TrimSpace(nativeID)
	if nativeID == "" {
		return
	}
	r := nexusOwned
	r.mu.Lock()
	r.loadLocked()
	if _, ok := r.ids[strings.ToLower(nativeID)]; ok {
		r.mu.Unlock()
		return
	}
	r.addLocked(nativeID, time.Now().UTC())
	if len(r.ids) > maxOwnedSessions {
		r.pruneLocked()
	}
	r.version++
	version := r.version
	snapshot := make(map[string]time.Time, len(r.ids))
	for k, v := range r.ids {
		snapshot[k] = v
	}
	path := r.path
	r.mu.Unlock()
	if path == "" {
		return
	}
	r.persist(path, version, snapshot)
}

// persist writes snapshot unless a newer one is already on disk. Each write
// uses its own temp file in the target directory, then renames it in place.
func (r *registry) persist(path string, version uint64, snapshot map[string]time.Time) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if version <= r.written {
		return
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0o700)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		log.Printf("owned sessions: %v", err)
		return
	}
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		log.Printf("owned sessions: %v", err)
		return
	}
	r.written = version
}

func (r *registry) pruneLocked() {
	type kv struct {
		id string
		at time.Time
	}
	var oldest kv
	for len(r.ids) > maxOwnedSessions {
		first := true
		for id, at := range r.ids {
			if first || at.Before(oldest.at) {
				oldest, first = kv{id, at}, false
			}
		}
		delete(r.ids, oldest.id)
	}
	r.short = map[string]bool{}
	for id := range r.ids {
		if u := uuidPattern.FindString(id); u != "" {
			r.short[u[:8]] = true
		}
	}
}

// IsSession reports whether a native session id belongs to Nexus.
func IsSession(nativeID string) bool {
	nativeID = strings.ToLower(strings.TrimSpace(nativeID))
	if nativeID == "" {
		return false
	}
	r := nexusOwned
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	_, ok := r.ids[nativeID]
	return ok
}

// IsTranscript reports whether a transcript path belongs to a chat Nexus
// already saves: Claude <id>.jsonl, Codex rollout-...-<id>.jsonl, Cursor
// chats/<hash>/<id>/, Kimi sessions/.../session_<id>/, Gemini
// session-<date>-<id[:8]>.jsonl.
func IsTranscript(path string) bool {
	r := nexusOwned
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadLocked()
	if len(r.ids) == 0 {
		return false
	}
	for _, id := range uuidPattern.FindAllString(path, -1) {
		id = strings.ToLower(id)
		if _, ok := r.ids[id]; ok {
			return true
		}
		if _, ok := r.ids["session_"+id]; ok { // Kimi: session_<uuid>
			return true
		}
	}
	if m := geminiShortID.FindStringSubmatch(filepath.Base(path)); m != nil && r.short[strings.ToLower(m[1])] {
		return true
	}
	return false
}

// ResetForTest drops the in-memory registry so the next call reloads it
// from StatePath.
func ResetForTest() {
	r := nexusOwned
	r.mu.Lock()
	r.loaded = false
	r.mu.Unlock()
	r.writeMu.Lock()
	r.written = 0
	r.writeMu.Unlock()
}
