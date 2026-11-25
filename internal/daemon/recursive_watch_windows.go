//go:build windows

package daemon

// One recursive change watch per root folder on Windows.
//
// fsnotify (v1.10) has no public recursive mode, so watching a tree meant
// one ReadDirectoryChangesW handle and one buffer per folder: 64 KiB each by
// default. Agent transcript trees have thousands of folders (6,600 on one
// real machine), which held hundreds of megabytes of heap for as long as the
// app ran. ReadDirectoryChangesW can watch a whole subtree with one handle
// and one buffer; that is used here. Ignored folders (node_modules, .git,
// build output) are filtered from the events instead of by not watching
// them. A buffer overflow is reported as fsnotify.ErrEventOverflow, which
// both callers answer with a full rescan.

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/windows"
)

// recursiveWatchSupported reports whether newRecursiveWatcher works here.
const recursiveWatchSupported = true

// recursiveWatchBuffer is the change buffer per watched root.
const recursiveWatchBuffer = 64 << 10

const recursiveWatchMask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
	windows.FILE_NOTIFY_CHANGE_SIZE | windows.FILE_NOTIFY_CHANGE_LAST_WRITE

type recursiveWatcher struct {
	events chan fsnotify.Event
	errors chan error
	done   chan struct{}

	mu     sync.Mutex
	roots  map[string]*recursiveRoot // lower-case clean path -> root
	closed bool
	wg     sync.WaitGroup
}

type recursiveRoot struct {
	dir  string
	h    windows.Handle
	stop windows.Handle // event: set to end the watch
	ov   *windows.Overlapped
	io   windows.Handle // event signalled when a read completes
}

func newRecursiveWatcher() (*recursiveWatcher, error) {
	return &recursiveWatcher{
		events: make(chan fsnotify.Event, 256),
		errors: make(chan error, 16),
		done:   make(chan struct{}),
		roots:  map[string]*recursiveRoot{},
	}, nil
}

func (w *recursiveWatcher) Events() <-chan fsnotify.Event { return w.events }
func (w *recursiveWatcher) Errors() <-chan error          { return w.errors }

// Len is the number of watched roots.
func (w *recursiveWatcher) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.roots)
}

// Covers reports whether dir is inside a watched root.
func (w *recursiveWatcher) Covers(dir string) bool {
	key := strings.ToLower(filepath.Clean(dir))
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.coversLocked(key)
}

func (w *recursiveWatcher) coversLocked(key string) bool {
	for root := range w.roots {
		if key == root || strings.HasPrefix(key, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// AddRoot watches dir and everything below it. A folder already inside a
// watched root costs nothing.
func (w *recursiveWatcher) AddRoot(dir string) error {
	dir = filepath.Clean(dir)
	key := strings.ToLower(dir)
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errors.New("recursive watcher closed")
	}
	if w.coversLocked(key) {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return err
	}
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return err
	}
	ioEv, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(stop)
		windows.CloseHandle(h)
		return err
	}
	r := &recursiveRoot{dir: dir, h: h, stop: stop, io: ioEv, ov: &windows.Overlapped{HEvent: ioEv}}

	w.mu.Lock()
	if w.closed || w.coversLocked(key) {
		w.mu.Unlock()
		r.close()
		return nil
	}
	// A new root may contain roots added earlier: they are now redundant.
	for k, old := range w.roots {
		if strings.HasPrefix(k, key+string(filepath.Separator)) {
			windows.SetEvent(old.stop)
			delete(w.roots, k)
		}
	}
	w.roots[key] = r
	w.wg.Add(1)
	w.mu.Unlock()
	go w.loop(r)
	return nil
}

func (r *recursiveRoot) close() {
	windows.CloseHandle(r.io)
	windows.CloseHandle(r.stop)
	windows.CloseHandle(r.h)
}

func (w *recursiveWatcher) send(ev fsnotify.Event) bool {
	select {
	case w.events <- ev:
		return true
	case <-w.done:
		return false
	}
}

func (w *recursiveWatcher) sendErr(err error) {
	select {
	case w.errors <- err:
	case <-w.done:
	default: // a pending error already asks for a rescan
	}
}

func (w *recursiveWatcher) loop(r *recursiveRoot) {
	defer w.wg.Done()
	defer r.close()
	// The kernel writes into buf and r.ov while a read is pending: both are
	// heap objects referenced until the read has completed or was cancelled.
	buf := make([]byte, recursiveWatchBuffer)
	for {
		windows.ResetEvent(r.io)
		if err := windows.ReadDirectoryChanges(r.h, &buf[0], uint32(len(buf)), true, recursiveWatchMask, nil, r.ov, 0); err != nil {
			w.sendErr(err)
			return
		}
		which, err := windows.WaitForMultipleObjects([]windows.Handle{r.io, r.stop}, false, windows.INFINITE)
		if err != nil || which != windows.WAIT_OBJECT_0 {
			_ = windows.CancelIoEx(r.h, r.ov)
			var n uint32
			_ = windows.GetOverlappedResult(r.h, r.ov, &n, true)
			return
		}
		var n uint32
		if err := windows.GetOverlappedResult(r.h, r.ov, &n, false); err != nil {
			if errors.Is(err, windows.ERROR_NOTIFY_ENUM_DIR) {
				w.sendErr(fsnotify.ErrEventOverflow)
				continue
			}
			w.sendErr(err) // the root was removed or became unreadable
			return
		}
		if n == 0 {
			w.sendErr(fsnotify.ErrEventOverflow)
			continue
		}
		if !w.deliver(r.dir, buf[:n]) {
			return
		}
	}
}

// deliver turns FILE_NOTIFY_INFORMATION records into events.
func (w *recursiveWatcher) deliver(root string, b []byte) bool {
	for off := 0; off+12 <= len(b); {
		info := (*windows.FileNotifyInformation)(unsafe.Pointer(&b[off]))
		nameLen := int(info.FileNameLength) / 2
		if off+12+int(info.FileNameLength) > len(b) {
			return true
		}
		name := windows.UTF16ToString(unsafe.Slice(&info.FileName, nameLen))
		ev := fsnotify.Event{Name: filepath.Join(root, name)}
		switch info.Action {
		case windows.FILE_ACTION_ADDED, windows.FILE_ACTION_RENAMED_NEW_NAME:
			ev.Op = fsnotify.Create
		case windows.FILE_ACTION_REMOVED:
			ev.Op = fsnotify.Remove
		case windows.FILE_ACTION_MODIFIED:
			ev.Op = fsnotify.Write
		case windows.FILE_ACTION_RENAMED_OLD_NAME:
			ev.Op = fsnotify.Rename
		}
		if ev.Op != 0 && !w.send(ev) {
			return false
		}
		if info.NextEntryOffset == 0 {
			return true
		}
		off += int(info.NextEntryOffset)
	}
	return true
}

// Close ends every watch and waits for them.
func (w *recursiveWatcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	for _, r := range w.roots {
		windows.SetEvent(r.stop)
	}
	w.roots = map[string]*recursiveRoot{}
	close(w.done)
	w.mu.Unlock()
	w.wg.Wait()
	return nil
}
