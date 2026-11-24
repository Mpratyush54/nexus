//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

func openFDs(t *testing.T) int {
	t.Helper()
	// Probe each descriptor number: reading /dev/fd is unreliable on macOS.
	n := 0
	for fd := 0; fd < 65536; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			n++
		}
	}
	return n
}

// makeTree builds dirs folders of files files each under root.
func makeTree(t *testing.T, root string, dirs, files int) {
	t.Helper()
	for d := 0; d < dirs; d++ {
		dir := filepath.Join(root, "pkg", fmt.Sprintf("d%03d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := 0; f < files; f++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.go", f)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// On kqueue (macOS) fsnotify holds one descriptor per watched entry. This
// measures that cost for real and proves the watcher stays in its budget.
func TestProjectWatcherDescriptorBudgetKqueue(t *testing.T) {
	if !watchCostsPerEntry {
		t.Skip("fsnotify does not spend a descriptor per entry on this OS")
	}
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err == nil {
		t.Logf("RLIMIT_NOFILE soft=%d hard=%d (Go raises soft at startup)", lim.Cur, lim.Max)
	}
	root := t.TempDir()
	const dirs, files = 80, 100 // 8,000 files + 81 folders
	makeTree(t, root, dirs, files)

	// Unbudgeted: what the watcher cost before the budget existed.
	before := openFDs(t)
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	raw := &projectWatcher{root: root, w: w, watched: map[string]bool{}}
	raw.addTree(root)
	rawCost := openFDs(t) - before
	_ = w.Close()
	t.Logf("unbudgeted watch of %d folders / %d files held %d descriptors", dirs+2, dirs*files, rawCost)
	if rawCost < dirs*files/2 {
		t.Logf("note: kqueue cost lower than expected (%d)", rawCost)
	}

	before = openFDs(t)
	w2, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	pw := &projectWatcher{root: root, w: w2, watched: map[string]bool{}, budget: kqueueWatchBudget}
	pw.addTree(root)
	cost := openFDs(t) - before
	t.Logf("budgeted watch held %d descriptors (budget %d), capped=%v", cost, kqueueWatchBudget, pw.capped)
	if cost > kqueueWatchBudget+16 {
		t.Fatalf("budgeted watcher held %d descriptors, budget is %d", cost, kqueueWatchBudget)
	}
	if !pw.capped {
		t.Fatal("a tree over budget must be reported as capped (disables git caching)")
	}
}
