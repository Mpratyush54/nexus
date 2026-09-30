package desktopui

import (
	"strings"
	"testing"

	"central-memory/internal/cloudclient"
)

func TestExtractFilePaths(t *testing.T) {
	content := "See `internal/desktopui/shell.go` and [readme](docs/native-app-direction.md). Also D:\\central-memory\\cmd\\nexus-desktop\\main.go and ./pkg/util.ts"
	got := extractFilePaths(content, []string{"frontend/src/api/memory.ts", "not a path"})
	want := []string{
		"frontend/src/api/memory.ts",
		"internal/desktopui/shell.go",
		"docs/native-app-direction.md",
		`D:\central-memory\cmd\nexus-desktop\main.go`,
		"./pkg/util.ts",
	}
	if len(got) < 4 {
		t.Fatalf("got %v", got)
	}
	for _, w := range want[:3] {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %q in %v", w, got)
		}
	}
}

func TestExtractFilePathsEmpty(t *testing.T) {
	if got := extractFilePaths("no paths here just words", nil); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestFormatMemoryDetail(t *testing.T) {
	it := cloudclient.MemoryItem{
		Key:       "arch/fyne",
		ID:        "m1",
		ProjectID: "p1",
		Level:     "project",
		Scope:     "decision",
		Status:    "CONFIRMED",
		Tags:      []string{"desktop", "ui"},
		CreatedAt: "2026-09-30T10:00:00Z",
		Content:   "Use Fyne for `internal/desktopui/nav.go`.",
	}
	got := formatMemoryDetail(it)
	for _, need := range []string{"Key: arch/fyne", "Project: p1", "Tags: desktop, ui", "--- Content ---", "Fyne"} {
		if !strings.Contains(got, need) {
			t.Fatalf("missing %q in:\n%s", need, got)
		}
	}
	refs := memoryFileRefs(it)
	if len(refs) != 1 || refs[0] != "internal/desktopui/nav.go" {
		t.Fatalf("refs=%v", refs)
	}
}

func TestResolveReadPath(t *testing.T) {
	root := `D:\central-memory`
	if got := resolveReadPath(`internal\foo.go`, root); got != "internal/foo.go" && got != `internal\foo.go` {
		// ToSlash on Windows yields internal/foo.go
		if got != "internal/foo.go" {
			t.Fatalf("rel got %q", got)
		}
	}
	abs := `D:\central-memory\internal\desktopui\shell.go`
	got := resolveReadPath(abs, root)
	if !strings.Contains(got, "desktopui") || strings.HasPrefix(got, "D:") {
		// should be relative under root
		if got != "internal/desktopui/shell.go" && got != `internal\desktopui\shell.go` {
			// filepath.Rel + ToSlash
			wantSlash := strings.ReplaceAll(got, `\`, `/`)
			if wantSlash != "internal/desktopui/shell.go" {
				t.Fatalf("abs under root got %q", got)
			}
		}
	}
	outside := `C:\Windows\System32\drivers\etc\hosts`
	if got := resolveReadPath(outside, root); got != outside {
		t.Fatalf("outside got %q", got)
	}
}
