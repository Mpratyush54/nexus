package continuex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceDiffsIncludeShellWritesDirtyNewAndDeletedFiles(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("dirty.txt", "user's pre-existing edit\n")
	write("deleted.txt", "removed\n")
	write("unchanged.txt", "same\n")
	before := captureWorkspaceBaseline(context.Background(), root)
	write("dirty.txt", "agent's new edit\n")
	write("added.txt", "created\n")
	if err := os.Remove(filepath.Join(root, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	changes := before.changes(captureWorkspaceBaseline(context.Background(), root))
	if len(changes) != 3 {
		t.Fatalf("changes=%+v", changes)
	}
	if changes[0].Status != "added" || !strings.Contains(changes[0].Diff, "+created") {
		t.Fatalf("new file=%+v", changes[0])
	}
	if changes[1].Status != "deleted" || !strings.Contains(changes[1].Diff, "-removed") {
		t.Fatalf("deleted file=%+v", changes[1])
	}
	if !strings.Contains(changes[2].Diff, "-user's pre-existing edit") || !strings.Contains(changes[2].Diff, "+agent's new edit") {
		t.Fatalf("dirty baseline lost=%+v", changes[2])
	}
}

func TestWorkspaceDiffsRedactSecretsAndExplainBinaryFiles(t *testing.T) {
	root := t.TempDir()
	before := captureWorkspaceBaseline(context.Background(), root)
	_ = os.WriteFile(filepath.Join(root, ".env"), []byte("PRIVATE_FIXTURE_VALUE"), 0600)
	_ = os.WriteFile(filepath.Join(root, "image.bin"), []byte{0, 1, 2}, 0600)
	_ = os.WriteFile(filepath.Join(root, "settings.txt"), []byte("api_key=PRIVATE_FIXTURE_VALUE\n"), 0600)
	changes := before.changes(captureWorkspaceBaseline(context.Background(), root))
	if len(changes) != 3 {
		t.Fatalf("changes=%+v", changes)
	}
	for _, change := range changes {
		if strings.Contains(change.Diff, "PRIVATE_FIXTURE_VALUE") {
			t.Fatal("secret leaked")
		}
		if change.Path != "settings.txt" && (change.Diff != "" || change.Notice == "") {
			t.Fatalf("missing omission notice=%+v", change)
		}
	}
}

func TestWorkspaceDiffsUseWorkingTreeNotHEADAndRespectGitIgnore(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "init", "--quiet", root)
	hideConsole(cmd)
	if err := cmd.Run(); err != nil {
		t.Skip("git unavailable")
	}
	_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0600)
	_ = os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("initial\n"), 0600)
	cmd = exec.Command("git", "add", ".")
	cmd.Dir = root
	hideConsole(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("user dirty\n"), 0600)
	before := captureWorkspaceBaseline(context.Background(), root)
	_ = os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("agent edit\n"), 0600)
	_ = os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("ignore\n"), 0600)
	changes := before.changes(captureWorkspaceBaseline(context.Background(), root))
	if len(changes) != 1 || !strings.Contains(changes[0].Diff, "-user dirty") || strings.Contains(changes[0].Diff, "-initial") {
		t.Fatalf("wrong baseline: %+v", changes)
	}
}

func TestWorkspaceDiffIncompleteScanDoesNotInventAdditionsOrDeletions(t *testing.T) {
	before := workspaceBaseline{complete: false, files: map[string]workspaceFile{"old.txt": {text: "old", preview: true}}}
	after := workspaceBaseline{complete: false, files: map[string]workspaceFile{"new.txt": {text: "new", preview: true}}}
	if changes := before.changes(after); len(changes) != 0 {
		t.Fatalf("incomplete scan invented changes: %+v", changes)
	}
}

func TestWorkspacePreviewBudgetDoesNotMarkUnchangedFilesModified(t *testing.T) {
	before := workspaceBaseline{complete: true, files: map[string]workspaceFile{"same.txt": {text: "same", fingerprint: "4:100", preview: true}}}
	after := workspaceBaseline{complete: true, files: map[string]workspaceFile{"same.txt": {fingerprint: "4:100", notice: "budget reached"}}}
	if changes := before.changes(after); len(changes) != 0 {
		t.Fatalf("budget invented changes: %+v", changes)
	}
}

func TestHeadlessDiffsDoNotDependOnToolEvents(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "shell-edited.txt")
	if err := os.WriteFile(file, []byte("user dirty baseline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NX_WORKSPACE_DIFF_HELPER", "1")
	var fileEvent bool
	output, err := (HeadlessExecutor{}).TurnStreaming(context.Background(), LaunchPlan{Agent: "cursor", Mode: "here", Argv: []string{os.Args[0], "-test.run=^TestWorkspaceDiffHelperProcess$"}}, root, func(event StreamProgress) {
		if event.Kind == "files" {
			fileEvent = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !fileEvent || len(output.FileDiffs) != 1 || !strings.Contains(output.FileDiffs[0].Diff, "-user dirty baseline") || !strings.Contains(output.FileDiffs[0].Diff, "+written without tool events") {
		t.Fatalf("headless shell/file diff lost: event=%t output=%+v", fileEvent, output)
	}
}

func TestWorkspaceDiffHelperProcess(t *testing.T) {
	if os.Getenv("NX_WORKSPACE_DIFF_HELPER") != "1" {
		return
	}
	if err := os.WriteFile("shell-edited.txt", []byte("written without tool events\n"), 0600); err != nil {
		os.Exit(2)
	}
	data, _ := json.Marshal(map[string]string{"type": "result", "result": "Completed"})
	fmt.Println(string(data))
	os.Exit(0)
}
