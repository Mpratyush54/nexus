package continuex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixture outputs of each agent's listing command, as the real CLIs print
// them (ANSI colour, log noise, CRLF).
var modelListFixtures = map[string]string{
	"opencode": "\x1b[90mINFO  2026-11-26 service=models loading\x1b[0m\r\nopencode/big-pickle\r\nanthropic/claude-sonnet-5-5\r\nopenai/gpt-6.2-sol\r\nnvidia/meta/llama-3.2-11b-vision-instruct\r\n",
	"cursor":   "Loading models…\n\x1b[1mAvailable models\x1b[0m\n\nauto - Auto (current, default)\ncomposer-2.5 - Composer 2.5\ngpt-6.2-sol-high - GPT-6.2 Sol High\nclaude-opus-5-thinking-high - Claude Opus 5 Thinking High\ngemini-3.1-pro - Gemini 3.1 Pro\n\nTip: use --model <id> to switch.\n",
}

// TestModelListFixtureChild plays an agent CLI: it prints the fixture for
// NX_MODEL_FIXTURE, or hangs when that is "hang".
func TestModelListFixtureChild(t *testing.T) {
	mode := os.Getenv("NX_MODEL_FIXTURE")
	switch mode {
	case "":
		return
	case "hang":
		time.Sleep(time.Minute)
	case "fail":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(3)
	default:
		fmt.Print(modelListFixtures[mode])
	}
	os.Exit(0)
}

func fixtureStatus(agent, name string) HarnessAvailability {
	return HarnessAvailability{Agent: agent, Name: name, Executable: os.Args[0], Prefix: []string{"-test.run=^TestModelListFixtureChild$", "--"}}
}

func TestOpenCodeListingFixtureGroupsByProviderWithFriendlyNames(t *testing.T) {
	t.Setenv("NX_MODEL_FIXTURE", "opencode")
	raw, err := readModelCommand(context.Background(), fixtureStatus("opencode", "OpenCode"), nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog := HarnessModelCatalog{Agent: "opencode", Items: parseOpenCodeModels(raw)}
	catalog.normalize()
	want := []HarnessModel{
		{ID: "opencode/big-pickle", Name: "Big Pickle", Provider: "OpenCode"},
		{ID: "anthropic/claude-sonnet-5-5", Name: "Claude Sonnet 5.5", Provider: "Anthropic"},
		{ID: "openai/gpt-6.2-sol", Name: "GPT-6.2 Sol", Provider: "OpenAI"},
		{ID: "nvidia/meta/llama-3.2-11b-vision-instruct", Name: "Llama 3.2 11b Vision Instruct", Provider: "NVIDIA"},
	}
	if len(catalog.Items) != len(want) {
		t.Fatalf("items: %+v", catalog.Items)
	}
	for i, item := range catalog.Items {
		if item != want[i] {
			t.Errorf("item %d = %+v, want %+v", i, item, want[i])
		}
	}
}

func TestCursorListingFixtureKeepsNamesDefaultAndProviders(t *testing.T) {
	t.Setenv("NX_MODEL_FIXTURE", "cursor")
	raw, err := readModelCommand(context.Background(), fixtureStatus("cursor", "Cursor"), nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog := HarnessModelCatalog{Agent: "cursor", Items: parseCursorModels(raw)}
	catalog.normalize()
	catalog.applyDefault("")
	var got []string
	for _, item := range catalog.Items {
		got = append(got, item.ID+"="+item.Name+"@"+item.Provider)
	}
	want := "auto=Auto@Cursor,composer-2.5=Composer 2.5@Cursor,gpt-6.2-sol-high=GPT-6.2 Sol High@OpenAI,claude-opus-5-thinking-high=Claude Opus 5 Thinking High@Anthropic,gemini-3.1-pro=Gemini 3.1 Pro@Google"
	if strings.Join(got, ",") != want {
		t.Fatalf("cursor rows:\n%s\nwant\n%s", strings.Join(got, ","), want)
	}
	if catalog.DefaultModel != "auto" || catalog.DefaultName != "Auto" {
		t.Fatalf("default %q %q", catalog.DefaultModel, catalog.DefaultName)
	}
}

func TestListingCommandTimesOutInsteadOfHanging(t *testing.T) {
	t.Setenv("NX_MODEL_FIXTURE", "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := readModelCommand(ctx, fixtureStatus("opencode", "OpenCode"), nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("listing ignored its timeout: %v", elapsed)
	}
}

func TestListingCommandFailureIsAnError(t *testing.T) {
	t.Setenv("NX_MODEL_FIXTURE", "fail")
	if _, err := readModelCommand(context.Background(), fixtureStatus("cursor", "Cursor"), nil); err == nil {
		t.Fatal("a failing CLI must be an error")
	}
}

func TestClaudeCatalogIsCuratedAndCurrent(t *testing.T) {
	catalog, err := DiscoverHarnessModels(context.Background(), "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"opus", "sonnet", "haiku", "fable", "opusplan", "claude-fable-5-2", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"} {
		found := false
		for _, item := range catalog.Items {
			found = found || item.ID == id
		}
		if !found {
			t.Errorf("missing %s", id)
		}
	}
	if catalog.Fallback || catalog.Warning != "" || catalog.Items[0].Provider != "Anthropic" {
		t.Fatalf("Claude's list is a normal source, not a fallback: %+v", catalog)
	}
}

func TestGeminiCatalogNeedsNoProcess(t *testing.T) {
	catalog, err := DiscoverHarnessModels(context.Background(), "gemini", false)
	if err != nil || len(catalog.Items) < 4 || catalog.Items[0].ID != "auto" || catalog.Warning != "" {
		t.Fatalf("gemini: %+v %v", catalog, err)
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredDefaultModelPerAgent(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{},"model":"claude-fable-5-2"}`)
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-6.1-sol\"\nprofile = \"deep\"\n[profiles.deep]\nmodel = \"gpt-6.2-astra\"\n[model_providers.x]\nenv_key = \"SECRET\"\n")
	writeFile(t, filepath.Join(home, ".config", "opencode", "opencode.jsonc"), "{\n  // my pick\n  \"$schema\": \"https://opencode.ai/config.json\",\n  /* block */ \"model\": \"anthropic/claude-sonnet-5-5\",\n  \"agent\": {\"plan\": {\"model\": \"x/y\",},},\n}\n")
	writeFile(t, filepath.Join(home, ".gemini", "settings.json"), `{"model":{"name":"gemini-3.5-flash"}}`)
	cases := map[string]string{"claude": "claude-fable-5-2", "codex": "gpt-6.2-astra", "opencode": "anthropic/claude-sonnet-5-5", "gemini": "gemini-3.5-flash", "cursor": ""}
	for agent, want := range cases {
		if got := configuredDefaultModel(agent, home, getenv); got != want {
			t.Errorf("%s default = %q, want %q", agent, got, want)
		}
	}
	env["ANTHROPIC_MODEL"] = "opus"
	env["GEMINI_MODEL"] = "pro"
	if got := configuredDefaultModel("claude", home, getenv); got != "opus" {
		t.Errorf("ANTHROPIC_MODEL should win, got %q", got)
	}
	if got := configuredDefaultModel("gemini", home, getenv); got != "pro" {
		t.Errorf("GEMINI_MODEL should win, got %q", got)
	}
	env["ANTHROPIC_MODEL"] = "default"
	if got := configuredDefaultModel("claude", home, getenv); got != "claude-fable-5-2" {
		t.Errorf("'default' is not a model, got %q", got)
	}
}

func TestApplyDefaultNamesOrAddsTheConfiguredModel(t *testing.T) {
	catalog := claudeModelCatalog()
	catalog.normalize()
	catalog.applyDefault("claude-fable-5-2")
	if catalog.DefaultModel != "claude-fable-5-2" || catalog.DefaultName != "Claude Fable 5.2" {
		t.Fatalf("default: %q %q", catalog.DefaultModel, catalog.DefaultName)
	}
	catalog.applyDefault("claude-opus-4-6")
	if catalog.Items[0].ID != "claude-opus-4-6" || !catalog.Items[0].Default || catalog.DefaultName != "Claude Opus 4.6" {
		t.Fatalf("unknown configured model should be added first: %+v", catalog.Items[0])
	}
	for _, item := range catalog.Items[1:] {
		if item.Default {
			t.Fatalf("only one default: %+v", item)
		}
	}
}

func TestCacheAppliesConfiguredDefaultOnEveryRead(t *testing.T) {
	fake := &fakeModelDiscovery{catalog: HarnessModelCatalog{Items: []HarnessModel{{ID: "gpt-6.2-sol", Name: "GPT-6.2-Sol", Default: true}, {ID: "gpt-6.1-sol"}}}}
	cache, _ := testModelCache(t, fake)
	configured := ""
	cache.defaultModel = func(string) string { return configured }
	got, err := cache.get(context.Background(), "codex", false)
	if err != nil || got.DefaultModel != "gpt-6.2-sol" || got.DefaultName != "GPT-6.2-Sol" {
		t.Fatalf("CLI default: %+v %v", got, err)
	}
	configured = "gpt-6.1-sol"
	got, _ = cache.get(context.Background(), "codex", false)
	if got.DefaultModel != "gpt-6.1-sol" || got.DefaultName != "GPT-6.1 Sol" || fake.calls.Load() != 1 {
		t.Fatalf("config default: %+v calls=%d", got, fake.calls.Load())
	}
}

func TestFallbackNeverReplacesARealList(t *testing.T) {
	fake := &fakeModelDiscovery{catalog: HarnessModelCatalog{Items: []HarnessModel{{ID: "real-model"}}}}
	cache, _ := testModelCache(t, fake)
	if _, err := cache.get(context.Background(), "claude", false); err != nil {
		t.Fatal(err)
	}
	fake.set(HarnessModelCatalog{Fallback: true, Warning: "probe timed out", Items: []HarnessModel{{ID: "builtin"}}}, nil)
	if _, err := cache.get(context.Background(), "claude", true); err != nil {
		t.Fatal(err)
	}
	got, _ := cache.get(context.Background(), "claude", false)
	if got.Models[0] != "real-model" || got.Warning != "" {
		t.Fatalf("fallback replaced the real list: %+v", got)
	}
}

func TestPrewarmFillsOnlyUsableAgentsInBackground(t *testing.T) {
	fake := &fakeModelDiscovery{catalog: HarnessModelCatalog{Items: []HarnessModel{{ID: "warm"}}}}
	cache, dir := testModelCache(t, fake)
	cache.prewarm([]HarnessAvailability{{Agent: "opencode", Usable: true}, {Agent: "gemini", Usable: false}, {Agent: "copilot", Usable: true}})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "opencode.json")); err == nil && !cache.prewarming.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fake.calls.Load() != 1 {
		t.Fatalf("prewarm discovered %d agents, want 1", fake.calls.Load())
	}
	// A fresh entry is not discovered again.
	cache.prewarm([]HarnessAvailability{{Agent: "opencode", Usable: true}})
	for cache.prewarming.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.calls.Load() != 1 {
		t.Fatal("prewarm re-ran a fresh entry")
	}
}

func TestDiscoveryErrorWithoutCacheIsReturnedAndBackedOff(t *testing.T) {
	fake := &fakeModelDiscovery{err: errors.New("Cursor isn't signed in")}
	cache, _ := testModelCache(t, fake)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return base }
	for i := 0; i < 3; i++ {
		if _, err := cache.get(context.Background(), "cursor", false); err == nil {
			t.Fatal("expected an error the UI turns into a quiet Refresh affordance")
		}
	}
	cache.prewarm([]HarnessAvailability{{Agent: "cursor", Usable: true}})
	for cache.prewarming.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.calls.Load() != 1 {
		t.Fatalf("a failed listing was re-spawned %d times inside the backoff", fake.calls.Load())
	}
	// Refresh retries at once; after the backoff a normal open retries too.
	_, _ = cache.get(context.Background(), "cursor", true)
	cache.now = func() time.Time { return base.Add(modelFailureBackoff + time.Second) }
	fake.set(HarnessModelCatalog{Items: []HarnessModel{{ID: "auto"}}}, nil)
	if got, err := cache.get(context.Background(), "cursor", false); err != nil || got.Models[0] != "auto" || fake.calls.Load() != 3 {
		t.Fatalf("after backoff: %+v %v calls=%d", got, err, fake.calls.Load())
	}
}

func TestFriendlyModelNames(t *testing.T) {
	for id, want := range map[string]string{
		"claude-sonnet-4-5":                 "Claude Sonnet 4.5",
		"claude-sonnet-4-5-20250929":        "Claude Sonnet 4.5 20250929",
		"gpt-5.3-codex-high":                "GPT-5.3 Codex High",
		"opencode/deepseek-v4.1-flash-free": "DeepSeek V4.1 Flash Free",
		"provider/model[1m]":                "Model [1M]",
		"auto":                              "Auto",
		"gemini-2.5-pro":                    "Gemini 2.5 Pro",
	} {
		if got := friendlyModelName(id); got != want {
			t.Errorf("%s -> %q, want %q", id, got, want)
		}
	}
}

func TestStripJSONCKeepsStrings(t *testing.T) {
	got := string(stripJSONC([]byte(`{"url":"http://x//y", /* c */ "a":[1,2,],}`)))
	if got != `{"url":"http://x//y",  "a":[1,2]}` {
		t.Fatalf("%q", got)
	}
}
