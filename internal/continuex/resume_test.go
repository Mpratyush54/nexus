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
			"open_in_agent": {bin: "agent", flags: []string{"--resume"}},
			"here":          {bin: "agent", flags: []string{"-p", "--resume", "--output-format"}},
		},
		"cursor": {
			"open_in_agent": {bin: "agent", flags: []string{"--resume"}},
			"here":          {bin: "agent", flags: []string{"-p", "--resume"}},
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
