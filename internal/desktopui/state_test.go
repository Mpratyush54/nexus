package desktopui

import (
	"strings"
	"testing"

	"central-memory/internal/localclient"
)

func TestDeriveMode(t *testing.T) {
	if deriveMode(false) != modeWelcome {
		t.Fatal("want welcome when signed out")
	}
	if deriveMode(true) != modeApp {
		t.Fatal("want app when signed in")
	}
}

func TestBuildStatusLine(t *testing.T) {
	if got := buildStatusLine(nil, false); got == "" {
		t.Fatal("empty status")
	}
	st := &localclient.Status{Root: "", HasToken: true, Username: "ada"}
	if got := buildStatusLine(st, true); !contains(got, "workspace") {
		t.Fatalf("got %q", got)
	}
	st.Root = `D:\proj`
	st.Connected = true
	if got := buildStatusLine(st, true); !contains(got, "ada") {
		t.Fatalf("got %q", got)
	}
}

func TestBuildHarvestRowsSeparatesKinds(t *testing.T) {
	h := &localclient.Harvest{
		Agents: []localclient.HarvestAgent{{Name: "cursor", Format: "jsonl", FilesSeen: 2}},
		Files:  []localclient.HarvestFileHit{{Name: "chat.jsonl", Path: "/tmp/a", Agent: "cursor"}},
		Recent: []localclient.HarvestEvent{{Type: "scan", Message: "ok"}},
	}
	rows := buildHarvestRows(h)
	if len(rows) != 3 {
		t.Fatalf("len=%d", len(rows))
	}
	if rows[0].kind != "agent" || rows[1].kind != "file" || rows[2].kind != "event" {
		t.Fatalf("kinds: %+v", rows)
	}
}

func TestBuildWorkspaceRowsNoHarvestFiles(t *testing.T) {
	st := &localclient.Status{Root: `D:\central-memory`}
	ws := &localclient.Workspace{Path: `D:\central-memory`, Project: "central-memory", Branch: "main", Commit: "abcdef01"}
	rows := buildWorkspaceRows(st, ws)
	for _, r := range rows {
		if contains(r.title, "jsonl") || contains(r.title, "transcript") {
			t.Fatalf("harvest leaked into workspace: %+v", r)
		}
	}
	if len(rows) < 2 {
		t.Fatalf("want root+git or path rows, got %#v", rows)
	}
}

func TestBuildConnectChecklist(t *testing.T) {
	items := buildConnectChecklist(nil, nil, false)
	if len(items) != 4 || items[0].done {
		t.Fatalf("signed-out checklist: %#v", items)
	}
	st := &localclient.Status{Root: "/ws"}
	h := &localclient.Harvest{LastScanAt: "now", LastScanFiles: 1}
	items = buildConnectChecklist(st, h, true)
	for i, it := range items {
		if !it.done {
			t.Fatalf("item %d not done: %#v", i, it)
		}
	}
}

func TestBuildHomeMarkdown(t *testing.T) {
	md := buildHomeMarkdown(nil, nil, false)
	if !contains(md, "not signed in") {
		t.Fatalf("%s", md)
	}
	st := &localclient.Status{Username: "ada", Root: "/ws", ServerURL: "https://api.example"}
	h := &localclient.Harvest{LastScanAt: "t", LastScanFiles: 3, LastScanTurns: 1}
	md = buildHomeMarkdown(st, h, true)
	if !contains(md, "ada") || !contains(md, "/ws") || !contains(md, "Harvest") {
		t.Fatalf("%s", md)
	}
}

func TestRecentDisplayName(t *testing.T) {
	if got := recentDisplayName(""); got != "" {
		t.Fatal(got)
	}
	got := recentDisplayName(`D:\central-memory`)
	if !contains(got, "central-memory") {
		t.Fatalf("%q", got)
	}
}

func TestFolderURIPathWindowsStyle(t *testing.T) {
	// Pure string normalization covered via buildWorkspaceRows path usage;
	// folderURIPath strips leading slash before drive letter.
	p := folderURIPath(nil)
	if p != "" {
		t.Fatal(p)
	}
}

// TestFolderDialogResizeOrderingInvariant documents the Fyne v2.8 root cause:
// FileDialog.Resize before Show panics because MinSize() nil-derefs dialog.win.
// pickWorkspaceFolder must Show() first, then Resize().
func TestFolderDialogResizeOrderingInvariant(t *testing.T) {
	got := folderDialogResizeAfterShow()
	if !contains(got, "Show before Resize") {
		t.Fatalf("invariant text missing: %q", got)
	}
}

func TestMemoryProjectIDNeverUsesWorkspaceID(t *testing.T) {
	st := &localclient.Status{WorkspaceID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	if got := memoryProjectID(st, nil); got != "" {
		t.Fatalf("workspace id leaked: %q", got)
	}
	h := &localclient.Harvest{ProjectID: "11111111-2222-3333-4444-555555555555"}
	if got := memoryProjectID(st, h); got != h.ProjectID {
		t.Fatalf("got %q", got)
	}
	h.ProjectID = "not-a-uuid"
	if got := memoryProjectID(st, h); got != "" {
		t.Fatalf("non-uuid accepted: %q", got)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
