package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"central-memory/internal/blobs"
	"central-memory/internal/cache"
	"central-memory/internal/continuex"
	"central-memory/internal/idehistory"
	"central-memory/internal/outbox"
	"central-memory/internal/secrets"
	"central-memory/internal/teleport"
)

type recordingCloud struct {
	mu    sync.Mutex
	posts []string
}

type routeCloud struct {
	method string
	path   string
	body   []byte
	result []byte
}

type recordingTurn struct {
	plan      continuex.LaunchPlan
	workspace string
}

type streamingTurn struct{ recordingTurn }

func (s *streamingTurn) TurnStreaming(_ context.Context, plan continuex.LaunchPlan, workspace string, progress func(continuex.StreamProgress)) (continuex.TurnOutput, error) {
	s.plan, s.workspace = plan, workspace
	progress(continuex.StreamProgress{Kind: "activity", Text: `{"id":"cursor-edit","tool":"edit","status":"completed","files":["src/main.go"],"diff":"-old\n+new"}`})
	progress(continuex.StreamProgress{Kind: "text", Text: "partial reply"})
	return continuex.TurnOutput{Text: "full reply", NativeID: "local-native-1", ChangedFiles: []string{"src/main.go"}, FileDiffs: []continuex.FileDiff{{Path: "src/main.go", Status: "modified", Diff: "@@ -1 +1 @@\n-old\n+new"}}}, nil
}

func (r *recordingTurn) Turn(_ context.Context, plan continuex.LaunchPlan, workspace string) (string, error) {
	r.plan, r.workspace = plan, workspace
	return "local reply", nil
}

func TestContinueHereSeedsRemoteOpenCodeChatInSelectedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	continuations := t.TempDir()
	executor := &recordingTurn{}
	args, _ := json.Marshal(map[string]any{
		"session_id": "cloud-chat", "native_id": "remote-only-id", "agent": "opencode",
		"resume": "seeded", "prompt": "continue", "context": "earlier chat",
		"workspace_root": workspace,
	})
	deps := Deps{Root: t.TempDir(), ContinueExecutor: executor, ContinuationDir: continuations}
	result, err := Call(context.Background(), "continue.here", args, deps)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	if m["text"] != "local reply" || m["saved_locally"] != true {
		t.Fatalf("send result=%v", m)
	}
	if executor.plan.Argv[0] != "opencode" || executor.plan.Native || executor.workspace != workspace {
		t.Fatalf("wrong bounded continuation: %+v in %q", executor.plan, executor.workspace)
	}
	loaded, err := Call(context.Background(), "continue.local.list", json.RawMessage(`{"session_id":"cloud-chat"}`), deps)
	if err != nil {
		t.Fatal(err)
	}
	items := loaded.([]ContinuationExchange)
	if len(items) != 1 || items[0].Prompt != "continue" || items[0].Answer != "local reply" {
		t.Fatalf("persisted conversation=%+v", items)
	}
}

func TestContinueHereSyncsTurnPairToCapturedSession(t *testing.T) {
	workspace := t.TempDir()
	cloud := &routeCloud{result: []byte(`{"created":2,"duplicates":0}`)}
	deps := Deps{Cloud: cloud, Online: true, ContinueExecutor: &recordingTurn{}, ContinuationDir: t.TempDir()}
	args, _ := json.Marshal(map[string]any{"session_id": "captured-1", "agent": "opencode", "resume": "seeded", "prompt": "next", "workspace_root": workspace})
	result, err := Call(context.Background(), "continue.here", args, deps)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["cloud_saved"] != true || cloud.method != "POST" || cloud.path != "/v1/agent-sessions/captured-1/turns/batch" {
		t.Fatalf("sync result=%v route=%s %s", result, cloud.method, cloud.path)
	}
	var payload struct {
		Turns []struct {
			Role   string `json:"role"`
			Text   string `json:"text_preview"`
			Source string `json:"source_ts"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(cloud.body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Turns) != 2 || payload.Turns[0].Role != "user" || payload.Turns[1].Text != "local reply" {
		t.Fatalf("payload=%+v", payload)
	}
	if payload.Turns[0].Source == "" || payload.Turns[1].Source == "" {
		t.Fatal("in-app activity lost original timestamps")
	}
	loaded, err := loadContinuations(deps.ContinuationDir, "captured-1")
	if err != nil || len(loaded) != 1 || !loaded[0].CloudSaved {
		t.Fatalf("sync marker=%+v %v", loaded, err)
	}
}

func TestContinueHereStreamsAndRestoresNativeSession(t *testing.T) {
	workspace := t.TempDir()
	store := t.TempDir()
	executor := &streamingTurn{}
	var request string
	var progress continuex.StreamProgress
	deps := Deps{ContinueExecutor: executor, ContinuationDir: store, ContinueProgress: func(id string, update continuex.StreamProgress) { request, progress = id, update }}
	args, _ := json.Marshal(map[string]any{"session_id": "captured", "request_id": "send-1", "agent": "opencode", "resume": "seeded", "prompt": "hello", "workspace_root": workspace})
	first, err := Call(context.Background(), "continue.here", args, deps)
	if err != nil {
		t.Fatal(err)
	}
	if request != "send-1" || progress.Kind != "text" || progress.Text != "partial reply" || first.(map[string]any)["native_id"] != "local-native-1" {
		t.Fatalf("stream/result=%v %q %q", first, request, progress)
	}
	if files, ok := first.(map[string]any)["file_changes"].([]string); !ok || len(files) != 1 || files[0] != "src/main.go" {
		t.Fatalf("missing workspace change: %v", first)
	}
	items, err := loadContinuations(store, "captured")
	if err != nil || len(items) != 1 || items[0].NativeID != "local-native-1" {
		t.Fatalf("restored native=%+v %v", items, err)
	}
	if len(items[0].Activities) != 1 || items[0].Activities[0].Diff != "-old\n+new" {
		t.Fatalf("tool details lost after reopening: %+v", items[0].Activities)
	}
	if len(items[0].FileDiffs) != 1 || !strings.Contains(items[0].FileDiffs[0].Diff, "+new") {
		t.Fatalf("file diff lost on reopen: %+v", items[0].FileDiffs)
	}
	args, _ = json.Marshal(map[string]any{"session_id": "captured", "request_id": "send-2", "agent": "opencode", "resume": "native", "native_id": items[0].NativeID, "prompt": "again", "workspace_root": workspace})
	if _, err := Call(context.Background(), "continue.here", args, deps); err != nil {
		t.Fatal(err)
	}
	if !executor.plan.Native || !strings.Contains(strings.Join(executor.plan.Argv, " "), "local-native-1") {
		t.Fatalf("native resume plan=%+v", executor.plan)
	}
}

func TestContinueCancelTargetsOnlyRequestedTurn(t *testing.T) {
	var called string
	deps := Deps{ContinueCancel: func(id string) bool { called = id; return id == "active" }}
	result, err := Call(context.Background(), "continue.cancel", json.RawMessage(`{"request_id":"active"}`), deps)
	if err != nil || called != "active" || result.(map[string]any)["cancelled"] != true {
		t.Fatalf("cancel=%v called=%q err=%v", result, called, err)
	}
	if _, err := Call(context.Background(), "continue.cancel", json.RawMessage(`{}`), deps); err == nil {
		t.Fatal("missing request ID must fail")
	}
}

func TestContinueHereSyncsFileDiffNotJustFilename(t *testing.T) {
	cloud := &routeCloud{}
	deps := Deps{ContinueExecutor: &streamingTurn{}, ContinuationDir: t.TempDir(), Cloud: cloud, Online: true}
	args, _ := json.Marshal(map[string]any{"session_id": "captured", "agent": "cursor", "resume": "seeded", "prompt": "edit file", "workspace_root": t.TempDir()})
	if _, err := Call(context.Background(), "continue.here", args, deps); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Turns []struct {
			ToolCalls []struct {
				Args map[string]string `json:"args"`
			} `json:"tool_calls"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(cloud.body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Turns) != 2 || len(body.Turns[1].ToolCalls) != 1 || !strings.Contains(body.Turns[1].ToolCalls[0].Args["diff"], "+new") {
		t.Fatalf("cloud still receives path-only tool records: %s", cloud.body)
	}
}

func (r *routeCloud) Do(_ context.Context, method, path string, body []byte) ([]byte, error) {
	r.method, r.path = method, path
	r.body = append([]byte(nil), body...)
	if r.result != nil {
		return r.result, nil
	}
	return []byte(`{"items":[]}`), nil
}

func (r *recordingCloud) Do(_ context.Context, method, path string, body []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts = append(r.posts, method+" "+path)
	return []byte(`{"ok":true}`), nil
}

func TestContinueAndOffline(t *testing.T) {
	ResetRunsForTest()
	raw, _ := json.Marshal(map[string]string{"session_id": "s1", "mode": "here"})
	out, err := Call(context.Background(), "continue.start", raw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["op_id"] != "op_s1" {
		t.Fatalf("op = %v", m["op_id"])
	}
	if _, err := Call(context.Background(), "timeline.list", nil, Deps{}); err != ErrOffline {
		t.Fatalf("offline: %v", err)
	}
	if _, err := Call(context.Background(), "nope", nil, Deps{}); err != ErrUnknownMethod {
		t.Fatalf("unknown: %v", err)
	}
}

func TestCloudMethodsUseContractRoutes(t *testing.T) {
	c := &routeCloud{}
	deps := Deps{Cloud: c, Online: true}
	if _, err := Call(context.Background(), "sessions.create", json.RawMessage(`{"project_id":"p1","harness":"codex","native_id":"nexus-new-1","title":"New chat"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/v1/agent-sessions" || !strings.Contains(string(c.body), `"native_id":"nexus-new-1"`) {
		t.Fatalf("new chat route = %s %s %s", c.method, c.path, c.body)
	}
	if _, err := Call(context.Background(), "sessions.create", json.RawMessage(`{"project_id":"p1"}`), deps); err == nil {
		t.Fatal("new chat without harness/native ID should be rejected")
	}

	if _, err := Call(context.Background(), "timeline.list", json.RawMessage(`{"project":"p1","agent":"codex","q":"auth","limit":50}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "GET" || !strings.HasPrefix(c.path, "/v1/timeline?") || !strings.Contains(c.path, "project_id=p1") || !strings.Contains(c.path, "harness=codex") || !strings.Contains(c.path, "q=auth") || !strings.Contains(c.path, "limit=50") {
		t.Fatalf("timeline = %s %s", c.method, c.path)
	}

	if _, err := Call(context.Background(), "memory.search", json.RawMessage(`{"q":"hello","project":"p1","scope":"project"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/v1/agent/memory/search" {
		t.Fatalf("memory route = %s %s", c.method, c.path)
	}
	var memoryBody map[string]any
	if err := json.Unmarshal(c.body, &memoryBody); err != nil {
		t.Fatal(err)
	}
	if memoryBody["query"] != "hello" || memoryBody["project_id"] != "p1" || memoryBody["level"] != "project" {
		t.Fatalf("memory body = %#v", memoryBody)
	}
	if _, forwarded := memoryBody["q"]; forwarded {
		t.Fatalf("strict endpoint received alias: %#v", memoryBody)
	}
	if _, err := Call(context.Background(), "memory.search", json.RawMessage(`{"project_id":"p/1"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "GET" || c.path != "/v1/projects/p%2F1/knowledge?limit=50" {
		t.Fatalf("memory default route = %s %s", c.method, c.path)
	}
	if _, err := Call(context.Background(), "memory.search", json.RawMessage(`{"browse":true,"project_id":"p/1","q":"hello","limit":50,"offset":100}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "GET" || c.path != "/memory/search?limit=50&offset=100&project_id=p%2F1&q=hello" {
		t.Fatalf("memory browse route = %s %s", c.method, c.path)
	}
	// The desktop's tag filter rides on the existing GET /memory/search tags parameter.
	if _, err := Call(context.Background(), "memory.search", json.RawMessage(`{"browse":true,"project_id":"p1","q":"","limit":50,"offset":0,"tags":"release notes"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "GET" || c.path != "/memory/search?limit=50&offset=0&project_id=p1&tags=release+notes" {
		t.Fatalf("memory browse tag route = %s %s", c.method, c.path)
	}

	if _, err := Call(context.Background(), "sessions.turns", json.RawMessage(`{"session_id":"s/1"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "GET" || c.path != "/v1/agent-sessions/s%2F1/turns" {
		t.Fatalf("turns route = %s %s", c.method, c.path)
	}
	if _, err := Call(context.Background(), "sessions.turns", json.RawMessage(`{"session_id":"s/1","limit":100,"cursor":"170000000000000001"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "GET" || !strings.HasPrefix(c.path, "/v1/agent-sessions/s%2F1/fetch?") || !strings.Contains(c.path, "limit=100") || !strings.Contains(c.path, "cursor=170000000000000001") {
		t.Fatalf("paged turns route = %s %s", c.method, c.path)
	}
	for _, method := range []string{"sessions.files", "sessions.operations", "sessions.memories"} {
		if _, err := Call(context.Background(), method, json.RawMessage(`{"session_id":"s/1"}`), deps); err != nil {
			t.Fatal(err)
		}
		if c.method != "GET" || c.path != "/v1/agent-sessions/s%2F1/"+strings.TrimPrefix(method, "sessions.") {
			t.Fatalf("%s route = %s %s", method, c.method, c.path)
		}
	}
	if _, err := Call(context.Background(), "sessions.fork", json.RawMessage(`{"session_id":"s/1"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/v1/agent-sessions/s%2F1/fork" || string(c.body) != "{}" {
		t.Fatalf("fork route = %s %s %s", c.method, c.path, c.body)
	}
	if _, err := Call(context.Background(), "sessions.share", json.RawMessage(`{"session_id":"s/1","user_id":"friend"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "PUT" || c.path != "/v1/agent-sessions/s%2F1/grants/friend" || string(c.body) != `{"live":true}` {
		t.Fatalf("share route = %s %s %s", c.method, c.path, c.body)
	}
	if _, err := Call(context.Background(), "sessions.share", json.RawMessage(`{"session_id":"s/1","user_id":"friend","live":false}`), deps); err != nil {
		t.Fatal(err)
	}
	if string(c.body) != `{"live":false}` {
		t.Fatalf("point-in-time share body = %s", c.body)
	}
	if _, err := Call(context.Background(), "sessions.share", json.RawMessage(`{"session_id":"s/1","team":true,"confirm":true,"live":true}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "PUT" || c.path != "/v1/agent-sessions/s%2F1/grants/team" || string(c.body) != `{"confirm":true,"live":true}` {
		t.Fatalf("team share route = %s %s %s", c.method, c.path, c.body)
	}
	if _, err := Call(context.Background(), "sessions.share", json.RawMessage(`{"session_id":"s/1","organization_id":"org1","confirm":true}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/v1/agent-sessions/s%2F1/grants/organization" || string(c.body) != `{"confirm":true,"organization_id":"org1"}` {
		t.Fatalf("organization share route = %s %s %s", c.method, c.path, c.body)
	}
	if _, err := Call(context.Background(), "sharing.recipients", json.RawMessage(`{"project_id":"p/1"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "GET" || c.path != "/v1/sharing/recipients?project_id=p%2F1" {
		t.Fatalf("recipient directory route = %s %s", c.method, c.path)
	}
	if _, err := Call(context.Background(), "memory.pin", json.RawMessage(`{"id":"m/1"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/memory/m%2F1/pin" {
		t.Fatalf("pin route = %s %s", c.method, c.path)
	}
	if _, err := Call(context.Background(), "memory.forget", json.RawMessage(`{"id":"m/1"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/memory/m%2F1/forget" {
		t.Fatalf("forget route = %s %s", c.method, c.path)
	}

	c.result = []byte(`{"items":[{"harness":"codex","project_id":"p1"}]}`)
	if _, err := Call(context.Background(), "agents.list", nil, deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "GET" || c.path != "/v1/timeline?limit=100&include_subagents=1" {
		t.Fatalf("agents route = %s %s", c.method, c.path)
	}
	if _, err := Call(context.Background(), "agents.list", json.RawMessage(`{"project_id":"p/1"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "GET" || c.path != "/v1/timeline?limit=100&include_subagents=1&project_id=p%2F1" {
		t.Fatalf("scoped agents route = %s %s", c.method, c.path)
	}

	if _, err := Call(context.Background(), "teleport.apply", json.RawMessage(`{"id":"tp/1"}`), deps); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/v1/teleports/tp%2F1/accept" {
		t.Fatalf("teleport accept = %s %s", c.method, c.path)
	}
}

type testCaptureController struct {
	paused bool
}

func (c *testCaptureController) Pause() error {
	c.paused = true
	return nil
}

func (c *testCaptureController) Resume() error {
	c.paused = false
	return nil
}

func (c *testCaptureController) Status() any {
	return map[string]any{"paused": c.paused, "running": !c.paused}
}

func TestCaptureControlsDelegateToHost(t *testing.T) {
	capture := &testCaptureController{}
	deps := Deps{Capture: capture}

	paused, err := Call(context.Background(), "capture.pause", nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got := paused.(map[string]any)["paused"]; got != true || !capture.paused {
		t.Fatalf("pause response=%#v paused=%v", paused, capture.paused)
	}
	resumed, err := Call(context.Background(), "capture.resume", nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got := resumed.(map[string]any)["running"]; got != true || capture.paused {
		t.Fatalf("resume response=%#v paused=%v", resumed, capture.paused)
	}
}

func TestContinueStartNeverSpawnsRunner(t *testing.T) {
	ResetRunsForTest()
	runner := &continuex.Controllable{NextID: "op_runner_1"}
	raw, _ := json.Marshal(map[string]string{"session_id": "s9", "mode": "here", "prompt": "hi"})
	out, err := Call(context.Background(), "continue.start", raw, Deps{ContinueRunner: runner})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["op_id"] != "op_s9" {
		t.Fatalf("op = %v", m["op_id"])
	}
	if spawn, _ := m["spawn"].(bool); spawn {
		t.Fatal("continue.start must not spawn")
	}
	if runner.LastArgv() != nil {
		t.Fatalf("runner was invoked: %v", runner.LastArgv())
	}
	cmd, _ := m["command"].([]string)
	if len(cmd) == 0 {
		t.Fatal("missing command in result")
	}
}

func TestContinueStartNativeIDReturnsRealAgentCommand(t *testing.T) {
	ResetRunsForTest()
	out, err := Call(context.Background(), "continue.start", json.RawMessage(`{"session_id":"cloud-id","mode":"open_in_agent","agent":"codex","native_id":"native-id"}`), Deps{})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["command"].([]string)
	want := []string{"codex", "resume", "native-id"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("command = %v, want %v", got, want)
	}
	// Without a native id there is no agent command, and the desktop must
	// never be handed a `nexus continue` CLI command instead.
	if _, err := Call(context.Background(), "continue.start", json.RawMessage(`{"session_id":"cloud-id","mode":"open_in_agent","agent":"codex"}`), Deps{}); err == nil {
		t.Fatal("open_in_agent without a native id should fail, not return a nexus CLI command")
	}
}

func TestRunsApproveCancelTransitions(t *testing.T) {
	ResetRunsForTest()
	raw, _ := json.Marshal(map[string]string{"session_id": "run1", "mode": "here"})
	out, err := Call(context.Background(), "continue.start", raw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	opID := out.(map[string]any)["op_id"].(string)

	msgRaw, _ := json.Marshal(map[string]string{"op_id": opID, "message": "do it"})
	got, err := Call(context.Background(), "runs.message", msgRaw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["status"] != "awaiting_approval" {
		t.Fatalf("after message: %+v", got)
	}

	apRaw, _ := json.Marshal(map[string]string{"op_id": opID})
	got, err = Call(context.Background(), "runs.approve", apRaw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["status"] != "approved" {
		t.Fatalf("after approve: %+v", got)
	}

	// Fresh run for cancel path.
	ResetRunsForTest()
	out, err = Call(context.Background(), "continue.start", raw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	opID = out.(map[string]any)["op_id"].(string)
	cancelRaw, _ := json.Marshal(map[string]string{"op_id": opID})
	got, err = Call(context.Background(), "runs.cancel", cancelRaw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if got.(map[string]any)["status"] != "cancelled" {
		t.Fatalf("after cancel: %+v", got)
	}
	if _, err := Call(context.Background(), "runs.approve", cancelRaw, Deps{}); err == nil {
		t.Fatal("approve after cancel should fail")
	}
}

func TestCacheOutboxAndFile(t *testing.T) {
	var c cache.Cache
	c.Put("k", []byte("v"))
	sp, err := outbox.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sp.Enqueue("1", "turn", []byte("t")); err != nil {
		t.Fatal(err)
	}
	out, err := Call(context.Background(), "uploads.status", nil, Deps{Outbox: sp})
	if err != nil || out.(map[string]any)["pending"].(int) != 1 {
		t.Fatalf("pending: %v %v", out, err)
	}
	if _, err := Call(context.Background(), "cache.clear", nil, Deps{Cache: &c}); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 0 {
		t.Fatal("cache not cleared")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"path": "a.txt"})
	got, err := Call(context.Background(), "files.read", raw, Deps{Root: dir})
	if err != nil || got.(map[string]any)["content"] != "hello" {
		t.Fatalf("read: %v %v", got, err)
	}
	raw, _ = json.Marshal(map[string]string{"path": "../a.txt"})
	if _, err := Call(context.Background(), "files.read", raw, Deps{Root: dir}); err == nil {
		t.Fatal("escape")
	}
}

func TestSecretsRestoreLocal(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	plain := []byte("TOKEN=abc\n")
	ct, err := secrets.Encrypt(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{
		"path":           ".env",
		"ciphertext_b64": base64.StdEncoding.EncodeToString(ct),
		"data_key_b64":   base64.StdEncoding.EncodeToString(key),
	})
	out, err := Call(context.Background(), "secrets.restore", raw, Deps{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["restored"] != true {
		t.Fatalf("out=%v", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil || string(got) != string(plain) {
		t.Fatalf("file=%q err=%v", got, err)
	}
	if _, err := Call(context.Background(), "secrets.restore", json.RawMessage(`{"path":".env"}`), Deps{Root: dir}); err == nil {
		t.Fatal("expected clear error without data key")
	}
}

func TestUploadsRetryDrainsWhenOnline(t *testing.T) {
	sp, err := outbox.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.CaptureEnqueue(sp, "t1", "turn", "POST", "/v1/agent-sessions/s/turns", []byte(`{"idx":1}`)); err != nil {
		t.Fatal(err)
	}
	cloud := &recordingCloud{}
	out, err := Call(context.Background(), "uploads.retry", nil, Deps{Online: true, Cloud: cloud, Outbox: sp})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["pending"].(int) != 0 || m["uploaded"].(int) != 1 {
		t.Fatalf("retry result = %+v", m)
	}
	if len(cloud.posts) != 1 || cloud.posts[0] != "POST /v1/agent-sessions/s/turns" {
		t.Fatalf("posts = %+v", cloud.posts)
	}
	// Offline / no cloud: do not drain.
	if err := outbox.CaptureEnqueue(sp, "t2", "turn", "POST", "/v1/agent-sessions/s/turns", []byte(`{"idx":2}`)); err != nil {
		t.Fatal(err)
	}
	out, err = Call(context.Background(), "uploads.retry", nil, Deps{Online: false, Cloud: cloud, Outbox: sp})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["pending"].(int) != 1 {
		t.Fatalf("offline should leave pending: %+v", out)
	}
	if len(cloud.posts) != 1 {
		t.Fatalf("offline should not post: %+v", cloud.posts)
	}
}

func TestBindingsMatchIDL(t *testing.T) {
	root := filepath.Join("..", "..", "bindings")
	cs, err := os.ReadFile(filepath.Join(root, "csharp", "NxMethods.g.cs"))
	if err != nil {
		t.Fatal(err)
	}
	sw, err := os.ReadFile(filepath.Join(root, "swift", "NxMethods.swift"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range Methods() {
		if !strings.Contains(string(cs), name) || !strings.Contains(string(sw), name) {
			t.Fatalf("binding missing %s", name)
		}
	}
}

func TestAuthDesktopCallbackPersistsCredentials(t *testing.T) {
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", t.TempDir())
	t.Setenv("NEXUS_TOKEN", "")
	t.Setenv("CENTRAL_MEMORY_TOKEN", "")
	t.Setenv("CENTRAL_SERVER_TOKEN", "")

	loginArgs, _ := json.Marshal(map[string]string{
		"app_url":    "https://nexus.example",
		"server_url": "https://api.example",
	})
	login, err := Call(context.Background(), "auth.login", loginArgs, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	body := login.(map[string]any)
	authURL, _ := body["url"].(string)
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	state := parsed.Query().Get("state")
	if state == "" || body["redirect"] != "nexus://auth/callback" {
		t.Fatalf("login = %+v", body)
	}

	callback := "nexus://auth/callback#" + url.Values{
		"token":    {"desktop-token"},
		"user_id":  {"user-1"},
		"username": {"ada"},
		"state":    {state},
	}.Encode()
	callbackArgs, _ := json.Marshal(map[string]string{"url": callback})
	result, err := Call(context.Background(), "auth.callback", callbackArgs, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	status := result.(map[string]any)
	if status["signed_in"] != true || status["user_id"] != "user-1" || status["username"] != "ada" {
		t.Fatalf("status = %+v", status)
	}
	if _, leaked := status["token"]; leaked {
		t.Fatal("auth.status must not expose the token")
	}

	loggedOut, err := Call(context.Background(), "auth.logout", nil, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if loggedOut.(map[string]any)["signed_in"] != false {
		t.Fatalf("logout = %+v", loggedOut)
	}
	if _, err := Call(context.Background(), "auth.callback", callbackArgs, Deps{}); err == nil {
		t.Fatal("replayed callback should fail")
	}
}

func TestTeleportPreparePlanAndApply(t *testing.T) {
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{
		"session_id": "s-prepare",
		"from_os":    "mac",
		"to_os":      "unix",
		"from_home":  "/Users/a",
		"to_home":    "/home/b",
		"agent":      "claude",
		"commit":     "abc12345deadbeef",
		"secrets":    []string{".env"},
		"text":       "mail ada@ex.com",
		"home":       "/Users/a",
	})
	out, err := Call(context.Background(), "teleport.prepare", raw, Deps{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	plan, ok := out.(teleport.PreparePlan)
	if !ok {
		// json-shaped via map when? BuildPreparePlan returns struct directly.
		t.Fatalf("type %T", out)
	}
	if !plan.DryRun || !plan.Repo.Found || plan.Secrets.Mode != "names_only" {
		t.Fatalf("plan=%+v", plan)
	}
	if len(plan.Continue) < 2 || plan.Target.Mode != "worktree" {
		t.Fatalf("checklist incomplete: %+v", plan)
	}

	stub, err := Call(context.Background(), "teleport.apply", json.RawMessage(`{"session_id":"s1"}`), Deps{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	sm := stub.(map[string]any)
	if sm["applied"] != false {
		t.Fatalf("stub apply: %+v", sm)
	}
	if _, ok := sm["next_steps"]; !ok {
		t.Fatalf("missing next_steps: %+v", sm)
	}

	dest := filepath.Join(dir, "restore-out")
	content := []byte("hello teleport")
	sum := blobs.HashBytes(content)
	applyArgs, _ := json.Marshal(map[string]any{
		"session_id": "s1",
		"dest":       dest,
		"files": []map[string]any{{
			"path": "hello.txt", "sha256": sum, "size": len(content),
			"bytes_b64": base64.StdEncoding.EncodeToString(content),
		}},
	})
	applied, err := Call(context.Background(), "teleport.apply", applyArgs, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	am := applied.(map[string]any)
	if am["applied"] != true || am["verified"].(int) != 1 {
		t.Fatalf("apply: %+v", am)
	}
	got, err := os.ReadFile(filepath.Join(dest, "hello.txt"))
	if err != nil || string(got) != string(content) {
		t.Fatalf("restored=%q err=%v", got, err)
	}
}

func TestRestoreIDEHistoryCursor(t *testing.T) {
	idehistory.AllowlistedCursorVersions["0.0-core-test"] = true
	t.Cleanup(func() { delete(idehistory.AllowlistedCursorVersions, "0.0-core-test") })

	root := t.TempDir()
	chats := filepath.Join(root, "globalStorage")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(chats, "state.vscdb")
	if err := os.WriteFile(target, []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"agent":      "cursor",
		"backup_dir": filepath.Join(root, "bak"),
		"target_db":  target,
		"session_id": "core-sess-1",
		"version":    "0.0-core-test",
		"title":      "from core",
	})
	out, err := Call(context.Background(), "sessions.restore_ide_history", raw, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*idehistory.Result)
	if res.Mode != "sidecar" {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(res.SidecarPath); err != nil {
		t.Fatal(err)
	}

	_, err = Call(context.Background(), "sessions.restore_ide_history", json.RawMessage(`{"agent":"windsurf","session_id":"w","backup_dir":"/tmp","target_db":"/tmp/x"}`), Deps{})
	if err == nil || !strings.Contains(err.Error(), "experimental") {
		t.Fatalf("windsurf: %v", err)
	}
}

// Turn indexes are int64 values past float64 precision; a saved Nexus reply
// finds its turn by that exact index, so the pass-through must not round it.
func TestCloudPassThroughKeepsLargeTurnIndexes(t *testing.T) {
	cloud := &routeCloud{result: []byte(`{"items":[{"idx":179533922286017891,"role":"assistant"}],"next_cursor":"179533922286017890"}`)}
	out, err := Call(context.Background(), "sessions.turns", json.RawMessage(`{"session_id":"s1","limit":40,"direction":"backward"}`), Deps{Cloud: cloud, Online: true})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(out)
	if !strings.Contains(string(encoded), `"idx":179533922286017891`) {
		t.Fatalf("turn index rounded: %s", encoded)
	}
}
