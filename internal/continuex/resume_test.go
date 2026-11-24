package continuex

import (
	"strings"
	"testing"
)

func TestCommand(t *testing.T) {
	here, err := Command(Request{SessionID: "s1", Mode: "here", Resume: "fork", Prompt: "go"})
	if err != nil || here[0] != "nexus" || here[3] != "--session" {
		t.Fatalf("here: %v %v", here, err)
	}
	open, err := Command(Request{SessionID: "s1", Mode: "open_in_agent", Agent: "cursor"})
	if err != nil || open[3] != "cursor" {
		t.Fatalf("open: %v %v", open, err)
	}
	if _, err := Command(Request{SessionID: "s1", Mode: "nope"}); err == nil {
		t.Fatal("bad mode")
	}
}

func TestLaunchPlanMatrix(t *testing.T) {
	type want struct {
		bin   string
		flags []string
	}
	// Expected binary + characteristic flags per agent × mode (spec 5.1).
	matrix := map[string]map[string]want{
		"claude": {
			"open_in_agent": {bin: "claude", flags: []string{"--resume"}},
			"here":          {bin: "claude", flags: []string{"-p", "--resume", "--output-format"}},
		},
		"codex": {
			"open_in_agent": {bin: "codex", flags: []string{"resume"}},
			"here":          {bin: "codex", flags: []string{"exec", "resume", "--json"}},
		},
		"agent": {
			"open_in_agent": {bin: "cursor-agent", flags: []string{"--resume"}},
			"here":          {bin: "cursor-agent", flags: []string{"-p", "--resume", "--output-format"}},
		},
		"cursor": {
			"open_in_agent": {bin: "cursor-agent", flags: []string{"--resume"}},
			"here":          {bin: "cursor-agent", flags: []string{"-p", "--resume"}},
		},
		"gemini": {
			"open_in_agent": {bin: "gemini", flags: []string{"--resume"}},
			"here":          {bin: "gemini", flags: []string{"-r"}},
		},
		"agy": {
			"open_in_agent": {bin: "agy", flags: []string{"--conversation"}},
			"here":          {bin: "agy", flags: []string{"-p", "--conversation", "--output-format"}},
		},
		"copilot": {
			"open_in_agent": {bin: "copilot", flags: []string{"--resume="}},
			"here":          {bin: "copilot", flags: []string{"--resume="}},
		},
		"opencode": {
			"open_in_agent": {bin: "opencode", flags: []string{"-s"}},
			"here":          {bin: "opencode", flags: []string{"run", "-s", "--format"}},
		},
		"kimi": {
			"open_in_agent": {bin: "kimi", flags: []string{"--session"}},
			"here":          {bin: "kimi", flags: []string{"--session", "-p"}},
		},
		"hermes": {
			"open_in_agent": {bin: "hermes", flags: []string{"--resume"}},
			"here":          {bin: "hermes", flags: []string{"--resume", "-q", "--format"}},
		},
	}

	for _, agent := range NativeHarnesses {
		modes, ok := matrix[agent]
		if !ok {
			// "cursor" is an alias covered above; NativeHarnesses includes both.
			if agent == "cursor" {
				continue
			}
			t.Fatalf("missing matrix row for %s", agent)
		}
		for mode, w := range modes {
			plan, err := Plan(agent, mode, "ses_test", "noop")
			if err != nil {
				t.Fatalf("%s/%s: %v", agent, mode, err)
			}
			if !plan.Native || len(plan.Argv) == 0 || plan.Argv[0] != w.bin {
				t.Fatalf("%s/%s argv=%v want bin %s", agent, mode, plan.Argv, w.bin)
			}
			joined := strings.Join(plan.Argv, " ")
			for _, flag := range w.flags {
				if !strings.Contains(joined, flag) {
					t.Fatalf("%s/%s missing %q in %v", agent, mode, flag, plan.Argv)
				}
			}
			if !strings.Contains(joined, "ses_test") {
				t.Fatalf("%s/%s missing native id in %v", agent, mode, plan.Argv)
			}
		}
	}
}

func TestSeededChatUsesSourceHarnessWithoutRemoteSessionID(t *testing.T) {
	for _, tc := range []struct{ agent, bin, flag string }{
		{"opencode", "opencode", "--format"},
		{"cursor", "cursor-agent", "--output-format"},
		{"codex", "codex", "--json"},
		{"claude-code", "claude", "--output-format"},
	} {
		plan, err := SeededPlan(tc.agent, "hello", "prior answer", nil)
		if err != nil || plan.Native || plan.Argv[0] != tc.bin || !strings.Contains(strings.Join(plan.Argv, " "), tc.flag) {
			t.Fatalf("%s: %#v %v", tc.agent, plan, err)
		}
		joined := strings.Join(plan.Argv, " ")
		if !strings.Contains(joined, "prior answer") || !strings.Contains(joined, "hello") || strings.Contains(joined, "--resume") {
			t.Fatalf("seeded context missing or resumed remote id: %v", plan.Argv)
		}
	}
	if _, err := SeededPlan("cursor", "hello", "", []string{"image.png"}); err == nil {
		t.Fatal("Cursor must reject unsupported image attachment")
	}
}

func TestInAppStreamingFlagsAndModelSelection(t *testing.T) {
	for _, tc := range []struct{ agent, model, streamFlag string }{
		{"opencode", "anthropic/claude-sonnet-4", "--format"},
		{"cursor", "auto", "--stream-partial-output"},
		{"codex", "gpt-5", "--json"},
		{"claude", "sonnet", "--include-partial-messages"},
	} {
		plan, err := SeededPlan(tc.agent, "hello", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		plan, err = WithModel(plan, tc.model)
		if err != nil {
			t.Fatalf("%s: %v", tc.agent, err)
		}
		joined := strings.Join(plan.Argv, " ")
		if !strings.Contains(joined, tc.streamFlag) || !strings.Contains(joined, "--model "+tc.model) {
			t.Fatalf("%s: %v", tc.agent, plan.Argv)
		}
	}
	if _, err := WithModel(LaunchPlan{Agent: "opencode", Argv: []string{"opencode", "run", "hi"}}, "unqualified"); err == nil {
		t.Fatal("OpenCode must receive provider/model")
	}
}

func TestCursorPlansDoNotStartAutoUpdater(t *testing.T) {
	seeded, err := SeededPlan("cursor", "hello", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range []LaunchPlan{seeded} {
		if !strings.Contains(strings.Join(plan.Argv, " "), "--disable-auto-update") {
			t.Fatalf("Cursor plan can launch the updater: %v", plan.Argv)
		}
	}
	for _, mode := range []string{"here", "open_in_agent"} {
		plan, err := Plan("cursor", mode, "session-123", "hello")
		if err != nil || !strings.Contains(strings.Join(plan.Argv, " "), "--disable-auto-update") {
			t.Fatalf("Cursor %s plan can launch the updater: %v %v", mode, plan.Argv, err)
		}
	}
}
