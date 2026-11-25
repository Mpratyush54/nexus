package continuex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// Package managers install agent CLIs behind launchers that a process can't
// run directly without a shell:
//
//   - npm / yarn / pnpm write <bin>.cmd, <bin>.ps1 and an extensionless sh
//     script that call either the package's native binary or `node <entry>`;
//   - scoop writes <bin>.exe plus <bin>.shim ("path = ...") or a .cmd that
//     names the real program by absolute path;
//   - bun, volta and chocolatey write small .exe launchers that work as-is.
//
// resolveShim reads such a launcher (never runs it) and returns the program
// to start plus any leading arguments. It is pure file inspection, so it is
// tested on every OS with fixture layouts.

// errShimUnresolved means the launcher was found but nothing runnable behind it.
var errShimUnresolved = errors.New("launcher found but its program is missing")

const maxShimBytes = 64 << 10

var quotedPath = regexp.MustCompile(`"([^"\r\n]+)"`)

// resolveShim returns (program, prefixArgs, nil) for a launcher it can see
// through, or errShimUnresolved. agentBinary is the command name the agent is
// started with ("opencode", "cursor-agent", ...).
func resolveShim(path, agentBinary string) (string, []string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	dir := filepath.Dir(path)
	if ext == ".exe" {
		// scoop: <bin>.exe is a generic launcher; <bin>.shim names the target.
		if target := scoopShimTarget(strings.TrimSuffix(path, filepath.Ext(path)) + ".shim"); target != "" {
			return target, nil, nil
		}
		return path, nil, nil
	}
	if ext != ".cmd" && ext != ".bat" && ext != ".ps1" && ext != "" {
		return path, nil, nil
	}
	raw, err := readSmall(path)
	if err != nil {
		return "", nil, errShimUnresolved
	}
	if ext == "" && !looksLikeShellScript(raw) {
		// An extensionless file that isn't a script is the program itself.
		return path, nil, nil
	}
	targets, missing := shimTargets(string(raw), dir)
	// A native program behind the launcher wins: no Node, no console host.
	for _, t := range targets {
		if isExe(t) && !isNodeBinary(t) {
			return t, nil, nil
		}
	}
	for _, t := range targets {
		if isExe(t) || isNodeBinary(t) {
			continue
		}
		if native := nativeBehindPackage(t, agentBinary); native != "" {
			return native, nil, nil
		}
		if strings.EqualFold(filepath.Ext(t), ".cmd") || strings.EqualFold(filepath.Ext(t), ".bat") {
			// A launcher pointing at another launcher (scoop -> npm).
			if prog, prefix, err := resolveShim(t, agentBinary); err == nil {
				return prog, prefix, nil
			}
			continue
		}
		if isScriptEntry(t) {
			if node := findNode(dir); node != "" {
				return node, []string{t}, nil
			}
		}
	}
	// The launcher names a binary a postinstall step never created (npm
	// --ignore-scripts, bun): use the platform package it would have copied.
	for _, t := range missing {
		if native := nativeBehindPackage(t, agentBinary); native != "" {
			return native, nil, nil
		}
	}
	return "", nil, errShimUnresolved
}

// shimTargets lists the files a launcher refers to, in order: those that
// exist, and those that don't but sit in an existing folder. Relative
// references use the launcher folder (%dp0%, %~dp0, $basedir, $PSScriptRoot).
func shimTargets(text, dir string) (existing, missing []string) {
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		low := strings.ToLower(line)
		if line == "" || strings.HasPrefix(low, "rem ") || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "::") {
			continue
		}
		var candidates []string
		for _, m := range quotedPath.FindAllStringSubmatch(line, -1) {
			candidates = append(candidates, m[1])
		}
		// scoop .cmd: @"C:\...\app.exe" %*  (already quoted) or @C:\...\app.exe %*
		if len(candidates) == 0 && strings.HasPrefix(line, "@") {
			if f := strings.Fields(strings.TrimPrefix(line, "@")); len(f) > 0 {
				candidates = append(candidates, f[0])
			}
		}
		for _, c := range candidates {
			p := expandShimPath(c, dir)
			if p == "" || seen[strings.ToLower(p)] {
				continue
			}
			seen[strings.ToLower(p)] = true
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
				existing = append(existing, p)
			} else if err != nil && !isNodeBinary(p) {
				if d, derr := os.Stat(filepath.Dir(p)); derr == nil && d.IsDir() {
					missing = append(missing, p)
				}
			}
		}
	}
	return existing, missing
}

func expandShimPath(s, dir string) string {
	s = strings.TrimSpace(s)
	for _, token := range []string{"%~dp0", "%dp0%", "%DP0%", "$basedir", "${basedir}", "$PSScriptRoot", "%~dp0%"} {
		if strings.HasPrefix(s, token) {
			s = dir + string(filepath.Separator) + strings.TrimLeft(s[len(token):], `\/`)
			break
		}
	}
	s = strings.ReplaceAll(s, "$exe", ".exe")
	if strings.ContainsAny(s, "%$*?<>|") {
		return "" // an unexpanded variable such as %_prog%
	}
	s = filepath.FromSlash(s)
	if !filepath.IsAbs(s) {
		return ""
	}
	return filepath.Clean(s)
}

// scoopShimTarget reads `path = "C:\...\app.exe"` from a scoop .shim file.
func scoopShimTarget(shim string) string {
	raw, err := readSmall(shim)
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "path") {
			continue
		}
		target := filepath.Clean(strings.Trim(strings.TrimSpace(v), `"`))
		if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() && isExe(target) {
			return target
		}
	}
	return ""
}

// nativeBehindPackage finds the platform binary an npm package ships next to
// its JavaScript launcher: bin/<name>.exe in the package, or an optional
// platform package (<pkg>-windows-x64, @scope/<pkg>-win32-x64, ...) nested
// in it or hoisted beside it. Returns "" when the package has none.
func nativeBehindPackage(script, agentBinary string) string {
	root, name := packageRoot(script)
	if root == "" {
		return ""
	}
	names := map[string]bool{}
	for _, n := range []string{agentBinary, strings.TrimSuffix(filepath.Base(script), filepath.Ext(script)), filepath.Base(name)} {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			names[n] = true
		}
	}
	matches := func(p string) bool {
		return isExe(p) && names[strings.ToLower(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))]
	}
	var found []string
	seen := map[string]bool{}
	add := func(pattern string) {
		hits, _ := filepath.Glob(pattern)
		for _, h := range hits {
			if info, err := os.Stat(h); err == nil && info.Mode().IsRegular() && matches(h) && !seen[h] {
				seen[h] = true
				found = append(found, h)
			}
		}
	}
	add(filepath.Join(root, "bin", "*.exe"))
	add(filepath.Join(root, "node_modules", "*", "bin", "*.exe"))
	add(filepath.Join(root, "node_modules", "@*", "*", "bin", "*.exe"))
	// Hoisted platform packages sit beside the package: opencode-windows-x64
	// next to opencode-ai, @scope/tool-win32-x64 next to @scope/tool.
	parent := filepath.Dir(root) // node_modules or node_modules/@scope
	for n := range names {
		for _, dir := range []string{parent, filepath.Dir(parent)} {
			add(filepath.Join(dir, n+"-win*", "bin", "*.exe"))
			add(filepath.Join(dir, n+"-windows*", "bin", "*.exe"))
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.SliceStable(found, func(i, j int) bool { return platformRank(found[i]) < platformRank(found[j]) })
	return found[0]
}

// platformRank prefers the package for this CPU, and the "baseline" build
// (no AVX2 needed) only after the regular one.
func platformRank(p string) int {
	low := strings.ToLower(p)
	rank := 0
	arch := runtime.GOARCH
	switch {
	case arch == "arm64" && strings.Contains(low, "arm64"):
	case arch == "amd64" && (strings.Contains(low, "x64") || strings.Contains(low, "x86_64") || strings.Contains(low, "amd64")):
	case strings.Contains(low, "arm64") || strings.Contains(low, "x64"):
		rank += 10 // another CPU's build
	}
	if strings.Contains(low, "baseline") {
		rank++
	}
	if strings.Contains(low, "musl") || strings.Contains(low, "linux") || strings.Contains(low, "darwin") {
		rank += 100
	}
	return rank
}

// packageRoot walks up from a file inside node_modules to the folder holding
// its package.json, and returns that folder and the package name.
func packageRoot(file string) (string, string) {
	dir := filepath.Dir(file)
	for i := 0; i < 6; i++ {
		raw, err := readSmall(filepath.Join(dir, "package.json"))
		if err == nil {
			var pkg struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &pkg) == nil && pkg.Name != "" {
				return dir, pkg.Name
			}
		}
		next := filepath.Dir(dir)
		if next == dir || strings.EqualFold(filepath.Base(dir), "node_modules") {
			break
		}
		dir = next
	}
	return "", ""
}

func findNode(shimDir string) string {
	for _, name := range []string{"node.exe", "node"} {
		p := filepath.Join(shimDir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	if p, err := exec.LookPath("node"); err == nil {
		return p
	}
	return ""
}

func isExe(p string) bool { return strings.EqualFold(filepath.Ext(p), ".exe") }

func isNodeBinary(p string) bool {
	b := strings.ToLower(filepath.Base(p))
	return b == "node.exe" || b == "node"
}

func isScriptEntry(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".js", ".mjs", ".cjs":
		return true
	case "":
		raw, err := readSmall(p)
		return err == nil && bytes.HasPrefix(raw, []byte("#!")) && bytes.Contains(firstLine(raw), []byte("node"))
	}
	return false
}

func looksLikeShellScript(raw []byte) bool {
	return bytes.HasPrefix(raw, []byte("#!"))
}

func firstLine(raw []byte) []byte {
	if i := bytes.IndexByte(raw, '\n'); i >= 0 {
		return raw[:i]
	}
	return raw
}

func readSmall(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if info.Size() > maxShimBytes {
		// Big files are programs, not launchers; only peek at the start.
		buf := make([]byte, 512)
		n, _ := io.ReadFull(f, buf)
		return buf[:n], nil
	}
	return io.ReadAll(io.LimitReader(f, maxShimBytes))
}

// agentSearchDirs are the folders package managers put agent launchers in.
// They are searched when PATH (often stale: Nexus started before the agent
// was installed) does not name the agent.
func agentSearchDirs() []string {
	home, _ := os.UserHomeDir()
	appData := os.Getenv("APPDATA")
	local := os.Getenv("LOCALAPPDATA")
	if runtime.GOOS == "windows" {
		if appData == "" && home != "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		if local == "" && home != "" {
			local = filepath.Join(home, "AppData", "Local")
		}
	}
	var dirs []string
	add := func(parts ...string) {
		if parts[0] == "" {
			return
		}
		dirs = append(dirs, filepath.Join(parts...))
	}
	if v := os.Getenv("NPM_CONFIG_PREFIX"); v != "" {
		add(v)
		add(v, "bin")
	}
	if v := os.Getenv("PNPM_HOME"); v != "" {
		add(v)
	}
	if v := os.Getenv("BUN_INSTALL"); v != "" {
		add(v, "bin")
	}
	if v := os.Getenv("VOLTA_HOME"); v != "" {
		add(v, "bin")
	}
	if v := os.Getenv("SCOOP"); v != "" {
		add(v, "shims")
	}
	add(appData, "npm")
	add(local, "pnpm")
	add(local, "Volta", "bin")
	add(local, "Yarn", "bin")
	add(home, ".bun", "bin")
	add(home, "scoop", "shims")
	add(home, ".local", "bin")
	add(home, ".npm-global", "bin")
	add(home, ".yarn", "bin")
	add(home, ".opencode", "bin")
	add(home, ".kimi-code", "bin")
	add(home, ".cargo", "bin")
	add(local, "Programs", "cursor-agent")
	add(local, "cursor-agent")
	if pd := os.Getenv("ProgramData"); pd != "" {
		add(pd, "chocolatey", "bin")
		add(pd, "scoop", "shims")
	}
	if runtime.GOOS != "windows" {
		dirs = append(dirs, unixAgentDirs(home, os.Getenv)...)
	}
	return dirs
}

// lookAgent is exec.LookPath with a fallback to the usual install folders.
func lookAgent(binary string) (string, error) {
	ensureAgentPATH()
	return lookAgentIn(binary, exec.LookPath, agentSearchDirs())
}

func lookAgentIn(binary string, look func(string) (string, error), dirs []string) (string, error) {
	if p, err := look(binary); err == nil {
		return p, nil
	} else if strings.ContainsAny(binary, `/\`) {
		return "", err
	}
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = []string{".exe", ".cmd", ".bat"}
	}
	for _, d := range dirs {
		for _, ext := range exts {
			p := filepath.Join(d, binary+ext)
			if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
				return p, nil
			}
		}
	}
	return "", exec.ErrNotFound
}
