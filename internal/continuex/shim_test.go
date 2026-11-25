package continuex

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// write creates a fixture file (and its folders) under root.
func write(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// npm's cmd-shim output for a package whose bin is a native program.
const npmCmdNative = "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n" +
	"\"%dp0%\\node_modules\\opencode-ai\\bin\\opencode.exe\"   %*\r\n"

// npm's cmd-shim output for a package whose bin is a Node script.
func npmCmdNode(rel string) string {
	return "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\r\n" +
		"IF EXIST \"%dp0%\\node.exe\" (\r\n  SET \"_prog=%dp0%\\node.exe\"\r\n) ELSE (\r\n  SET \"_prog=node\"\r\n  SET PATHEXT=%PATHEXT:;.JS;=;%\r\n)\r\n\r\n" +
		"endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\"  \"%dp0%\\" + rel + "\" %*\r\n"
}

const nodeScript = "#!/usr/bin/env node\nrequire('child_process')\n"

func TestResolveShimNpmNativeBinary(t *testing.T) {
	dir := t.TempDir()
	native := write(t, dir, "node_modules/opencode-ai/bin/opencode.exe", "MZ")
	shim := write(t, dir, "opencode.cmd", npmCmdNative)
	got, prefix, err := resolveShim(shim, "opencode")
	if err != nil || got != native || len(prefix) != 0 {
		t.Fatalf("got %q %v %v, want %q", got, prefix, err, native)
	}
}

func TestResolveShimNpmNodeLauncherUsesNestedPlatformPackage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "node_modules/opencode-ai/package.json", `{"name":"opencode-ai","bin":{"opencode":"./bin/opencode"}}`)
	write(t, dir, "node_modules/opencode-ai/bin/opencode", nodeScript)
	baseline := write(t, dir, "node_modules/opencode-ai/node_modules/opencode-windows-x64-baseline/bin/opencode.exe", "MZ")
	regular := write(t, dir, "node_modules/opencode-ai/node_modules/opencode-windows-x64/bin/opencode.exe", "MZ")
	write(t, dir, "node_modules/opencode-ai/node_modules/opencode-windows-arm64/bin/opencode.exe", "MZ")
	shim := write(t, dir, "opencode.cmd", npmCmdNode(`node_modules\opencode-ai\bin\opencode`))
	got, prefix, err := resolveShim(shim, "opencode")
	if err != nil || len(prefix) != 0 {
		t.Fatalf("got %q %v %v", got, prefix, err)
	}
	want := regular
	if runtime.GOARCH == "arm64" {
		want = filepath.Join(dir, "node_modules", "opencode-ai", "node_modules", "opencode-windows-arm64", "bin", "opencode.exe")
	}
	if got != want {
		t.Fatalf("got %q, want %q (baseline %q is the fallback)", got, want, baseline)
	}
}

func TestResolveShimNpmMissingPostinstallBinaryFallsBackToPlatformPackage(t *testing.T) {
	// The launcher names bin/opencode.exe, which opencode-ai's postinstall
	// creates; with --ignore-scripts it is missing but the platform package
	// is installed.
	dir := t.TempDir()
	write(t, dir, "node_modules/opencode-ai/package.json", `{"name":"opencode-ai"}`)
	write(t, dir, "node_modules/opencode-ai/bin/.keep", "")
	baseline := write(t, dir, "node_modules/opencode-ai/node_modules/opencode-windows-x64-baseline/bin/opencode.exe", "MZ")
	shim := write(t, dir, "opencode.cmd", npmCmdNative)
	got, _, err := resolveShim(shim, "opencode")
	if err != nil || got != baseline {
		t.Fatalf("got %q %v, want %q", got, err, baseline)
	}
}

func TestResolveShimNpmHoistedPlatformPackage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "node_modules/opencode-ai/package.json", `{"name":"opencode-ai"}`)
	write(t, dir, "node_modules/opencode-ai/bin/opencode", nodeScript)
	native := write(t, dir, "node_modules/opencode-windows-x64/bin/opencode.exe", "MZ")
	if runtime.GOARCH == "arm64" {
		native = write(t, dir, "node_modules/opencode-windows-arm64/bin/opencode.exe", "MZ")
	}
	shim := write(t, dir, "opencode.cmd", npmCmdNode(`node_modules\opencode-ai\bin\opencode`))
	got, _, err := resolveShim(shim, "opencode")
	if err != nil || got != native {
		t.Fatalf("got %q %v, want %q", got, err, native)
	}
}

func TestResolveShimNpmScriptOnlyRunsNodeDirectly(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "node_modules/@google/gemini-cli/package.json", `{"name":"@google/gemini-cli","bin":{"gemini":"bundle/gemini.js"}}`)
	entry := write(t, dir, "node_modules/@google/gemini-cli/bundle/gemini.js", nodeScript)
	node := write(t, dir, "node.exe", "MZ")
	shim := write(t, dir, "gemini.cmd", npmCmdNode(`node_modules\@google\gemini-cli\bundle\gemini.js`))
	got, prefix, err := resolveShim(shim, "gemini")
	if err != nil || got != node || len(prefix) != 1 || prefix[0] != entry {
		t.Fatalf("got %q %v %v, want %q [%q]", got, prefix, err, node, entry)
	}
}

func TestResolveShimPnpm(t *testing.T) {
	dir := t.TempDir()
	entry := write(t, dir, "global/5/node_modules/kimi-cli/bin/kimi.js", nodeScript)
	write(t, dir, "global/5/node_modules/kimi-cli/package.json", `{"name":"kimi-cli"}`)
	node := write(t, dir, "node.exe", "MZ")
	shim := write(t, dir, "kimi.CMD", "@SETLOCAL\r\n"+
		"@IF NOT DEFINED NODE_PATH (\r\n  @SET \"NODE_PATH=C:\\pnpm\\global\\5\\node_modules\"\r\n)\r\n"+
		"@IF EXIST \"%~dp0\\node.exe\" (\r\n  \"%~dp0\\node.exe\"  \"%~dp0\\global\\5\\node_modules\\kimi-cli\\bin\\kimi.js\" %*\r\n"+
		") ELSE (\r\n  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n  node  \"%~dp0\\global\\5\\node_modules\\kimi-cli\\bin\\kimi.js\" %*\r\n)\r\n")
	got, prefix, err := resolveShim(shim, "kimi")
	if err != nil || got != node || len(prefix) != 1 || prefix[0] != entry {
		t.Fatalf("got %q %v %v", got, prefix, err)
	}
}

func TestResolveShimScoopShimFile(t *testing.T) {
	dir := t.TempDir()
	target := write(t, dir, "apps/opencode/current/opencode.exe", "MZ")
	launcher := write(t, dir, "shims/opencode.exe", "MZ")
	write(t, dir, "shims/opencode.shim", "path = \""+target+"\"\r\n")
	got, _, err := resolveShim(launcher, "opencode")
	if err != nil || got != target {
		t.Fatalf("got %q %v, want %q", got, err, target)
	}
}

func TestResolveShimScoopCmdWithAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	target := write(t, dir, "apps/claude/current/claude.exe", "MZ")
	shim := write(t, dir, "shims/claude.cmd", "@rem "+target+"\r\n@\""+target+"\" %*\r\n")
	got, _, err := resolveShim(shim, "claude")
	if err != nil || got != target {
		t.Fatalf("got %q %v, want %q", got, err, target)
	}
}

func TestResolveShimExeLaunchersRunAsIs(t *testing.T) {
	// bun (tool.exe + tool.bunx), Volta and Chocolatey launchers are real
	// programs that need no shell.
	for _, layout := range []struct{ name, exe, side string }{
		{"bun", ".bun/bin/codex.exe", ".bun/bin/codex.bunx"},
		{"volta", "Volta/bin/codex.exe", ""},
		{"chocolatey", "chocolatey/bin/codex.exe", ""},
	} {
		dir := t.TempDir()
		exe := write(t, dir, layout.exe, "MZ")
		if layout.side != "" {
			write(t, dir, layout.side, `@/node_modules/@openai/codex/bin/codex.js`)
		}
		got, prefix, err := resolveShim(exe, "codex")
		if err != nil || got != exe || len(prefix) != 0 {
			t.Fatalf("%s: got %q %v %v", layout.name, got, prefix, err)
		}
	}
}

func TestResolveShimPowerShellAndShScripts(t *testing.T) {
	dir := t.TempDir()
	native := write(t, dir, "node_modules/opencode-ai/bin/opencode.exe", "MZ")
	ps1 := write(t, dir, "opencode.ps1", "#!/usr/bin/env pwsh\n$basedir=Split-Path $MyInvocation.MyCommand.Definition -Parent\n"+
		"if ($MyInvocation.ExpectingInput) {\n  $input | & \"$basedir/node_modules/opencode-ai/bin/opencode.exe\"   $args\n} else {\n  & \"$basedir/node_modules/opencode-ai/bin/opencode.exe\"   $args\n}\nexit $LASTEXITCODE\n")
	sh := write(t, dir, "opencode", "#!/bin/sh\nbasedir=$(dirname \"$(echo \"$0\" | sed -e 's,\\\\,/,g')\")\n\n"+
		"exec \"$basedir/node_modules/opencode-ai/bin/opencode.exe\"   \"$@\"\n")
	for _, shim := range []string{ps1, sh} {
		got, _, err := resolveShim(shim, "opencode")
		if err != nil || got != native {
			t.Fatalf("%s: got %q %v, want %q", filepath.Base(shim), got, err, native)
		}
	}
}

func TestResolveShimUnresolvable(t *testing.T) {
	dir := t.TempDir()
	shim := write(t, dir, "opencode.cmd", npmCmdNative) // nothing installed behind it
	if _, _, err := resolveShim(shim, "opencode"); !errors.Is(err, errShimUnresolved) {
		t.Fatalf("err = %v", err)
	}
}

func TestDirectExecutableEveryAgentBehindNpmShim(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("launchers are only resolved on Windows")
	}
	for _, agent := range []string{"claude", "codex", "cursor", "opencode", "gemini", "kimi", "copilot"} {
		dir := t.TempDir()
		bin := agentBinary(agent)
		pkg := "pkg-" + bin
		native := write(t, dir, "node_modules/"+pkg+"/bin/"+bin+".exe", "MZ")
		shim := write(t, dir, bin+".cmd", strings.ReplaceAll(npmCmdNative, `opencode-ai\bin\opencode.exe`, pkg+`\bin\`+bin+".exe"))
		got, prefix, err := directExecutable(shim, agent)
		if err != nil || got != native || len(prefix) != 0 {
			t.Fatalf("%s: got %q %v %v, want %q", agent, got, prefix, err, native)
		}
	}
}

func TestDirectExecutableFailureIsPlainLanguage(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("launchers are only resolved on Windows")
	}
	dir := t.TempDir()
	shim := write(t, dir, "opencode.cmd", npmCmdNative)
	_, _, err := directExecutable(shim, "opencode")
	var ae *AgentError
	if err == nil || !errors.As(AsAgentError("opencode", "cannot_start", err), &ae) {
		t.Fatalf("err = %v", err)
	}
	shown := ae.Message()
	if shown != "Nexus couldn't start OpenCode on this computer." {
		t.Fatalf("shown %q", shown)
	}
	for _, jargon := range []string{"shim", "PATH", "CLI", "executable"} {
		if strings.Contains(shown, jargon) {
			t.Fatalf("message mentions %q: %q", jargon, shown)
		}
	}
	if !strings.Contains(err.Error(), DetailMarker) || !strings.Contains(err.Error(), shim) {
		t.Fatalf("detail for the log is missing: %q", err.Error())
	}
}

func TestLookAgentFallsBackToInstallFolders(t *testing.T) {
	dir := t.TempDir()
	name := "opencode"
	if runtime.GOOS == "windows" {
		write(t, dir, "npm/opencode.cmd", npmCmdNative)
		name = "opencode.cmd"
	} else {
		write(t, dir, "npm/opencode", "#!/bin/sh\n")
	}
	missing := func(string) (string, error) { return "", errors.New("not on PATH") }
	got, err := lookAgentIn("opencode", missing, []string{filepath.Join(dir, "nothing"), filepath.Join(dir, "npm")})
	if err != nil || got != filepath.Join(dir, "npm", name) {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := lookAgentIn("absent", missing, []string{filepath.Join(dir, "npm")}); err == nil {
		t.Fatal("found an agent that is not installed")
	}
}

func TestSignedInDetection(t *testing.T) {
	home := t.TempDir()
	none := func(string) string { return "" }
	if got := signedIn("gemini", home, none); got != "no" {
		t.Fatalf("gemini without credentials: %s", got)
	}
	write(t, home, ".gemini/oauth_creds.json", `{"x":1}`)
	if got := signedIn("gemini", home, none); got != "yes" {
		t.Fatalf("gemini with oauth creds: %s", got)
	}
	if got := signedIn("codex", home, none); got != "no" {
		t.Fatalf("codex without auth: %s", got)
	}
	if got := signedIn("codex", home, func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "set"
		}
		return ""
	}); got != "yes" {
		t.Fatalf("codex with key: %s", got)
	}
	for _, agent := range []string{"opencode", "claude", "cursor", "kimi"} {
		if got := signedIn(agent, home, none); got != "unknown" {
			t.Fatalf("%s must not be reported signed out without proof: %s", agent, got)
		}
	}
	home2 := t.TempDir()
	write(t, home2, ".gemini/settings.json", "{\n // comment\n \"security\": {\"auth\": {\"selectedType\": \"compute-default-credentials\"}}\n}")
	if got := signedIn("gemini", home2, none); got != "unknown" {
		t.Fatalf("gemini with a chosen auth type: %s", got)
	}
	ready := withSignIn(HarnessAvailability{Agent: "gemini", Usable: true, State: "ready"}, t.TempDir(), none)
	if ready.Usable || ready.State != "signed_out" || strings.Contains(ready.Problem, "CLI") {
		t.Fatalf("%+v", ready)
	}
}
