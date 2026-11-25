//go:build windows

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func makeTree(t *testing.T, root string, dirs int) {
	t.Helper()
	for i := 0; i < dirs/2; i++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("chat-%04d", i), "subagents"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func drainWatcher(w *fsnotify.Watcher) {
	go func() {
		for {
			select {
			case _, ok := <-w.Events:
				if !ok {
					return
				}
			case _, ok := <-w.Errors:
				if !ok {
					return
				}
			}
		}
	}()
}

// A 5,000-folder tree: per-folder fsnotify watches against one recursive
// watch, by watch count and heap held.
func TestRecursiveWatchCostOnABigTree(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, 5000)

	perFolder := func(buf int) (int, uint64) {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			t.Skip(err)
		}
		drainWatcher(w)
		count := 0
		alloc := allocDuring(func() {
			_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err == nil && d.IsDir() {
					if w.AddWith(p, fsnotify.WithBufferSize(buf)) == nil {
						count++
					}
				}
				return nil
			})
		})
		w.Close()
		return count, alloc
	}
	defCount, defAlloc := perFolder(64 << 10)
	smallCount, smallAlloc := perFolder(4 << 10)

	rw, err := newRecursiveWatcher()
	if err != nil {
		t.Fatal(err)
	}
	var recAlloc uint64
	recAlloc = allocDuring(func() {
		if err := rw.AddRoot(root); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("5000-folder tree: fsnotify default %d watches %.1f MiB; fsnotify 4 KiB %d watches %.1f MiB; recursive %d watch %.2f MiB",
		defCount, float64(defAlloc)/(1<<20), smallCount, float64(smallAlloc)/(1<<20), rw.Len(), float64(recAlloc)/(1<<20))
	if rw.Len() != 1 {
		t.Fatalf("recursive watches = %d, want 1", rw.Len())
	}
	if !raceEnabled && recAlloc > 1<<20 {
		t.Fatalf("recursive watch allocated %.1f MiB", float64(recAlloc)/(1<<20))
	}

	// A file written deep in the tree is reported.
	deep := filepath.Join(root, "chat-2499", "subagents", "agent-0123456789abcdef.jsonl")
	if err := os.WriteFile(deep, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for got := false; !got; {
		select {
		case ev := <-rw.Events():
			got = ev.Name == deep
		case err := <-rw.Errors():
			t.Fatalf("watch error: %v", err)
		case <-deadline:
			t.Fatal("no event for a file deep in the tree")
		}
	}
	// A folder inside the root needs no watch of its own.
	if err := rw.AddRoot(filepath.Join(root, "chat-0001")); err != nil || rw.Len() != 1 {
		t.Fatalf("nested root: len=%d err=%v", rw.Len(), err)
	}
	before := runtime.NumGoroutine()
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after >= before {
		t.Fatalf("goroutines %d -> %d after Close", before, after)
	}
}

// The shared scanner picks up a new transcript deep in a large tree from a
// change notification, with a single watch.
func TestSharedScannerRecursiveWatchSeesNewTranscript(t *testing.T) {
	root := t.TempDir()
	makeTree(t, root, 2000)
	s := NewSharedScanner("")
	s.sourceList = []TranscriptSource{{Agent: "claude", Dirs: []string{root}, Format: FormatJSONL, Global: true}}
	var got []Event
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	h := NewHarvesterWithPoll(root, sliceEmitterFunc(func(ev Event) {
		<-mu
		got = append(got, ev)
		mu <- struct{}{}
	}), time.Hour, time.Hour)
	s.Attach(h)
	defer s.Detach(h)
	time.Sleep(500 * time.Millisecond) // first reconcile
	if s.rwatch == nil || s.rwatch.Len() != 1 {
		t.Fatalf("recursive watch not in use")
	}
	p := filepath.Join(root, "chat-0999", "subagents", "agent-0123456789abcdef.jsonl")
	writeLines(t, p, claudeUserLine(1, "a sub-agent task"))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		<-mu
		n := len(got)
		mu <- struct{}{}
		if n > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("new transcript not harvested from a change notification")
}

type sliceEmitterFunc func(Event)

func (f sliceEmitterFunc) Emit(ev Event) { f(ev) }
