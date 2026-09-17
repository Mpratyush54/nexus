// Command mem — central memory control plane (Go, stdlib only).
//
// P1 commands: init, remember, recall, sync, status, doctor.
// P2+: harvest, sessions, restore, backup, run, mcp serve, login (P3/P4).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"central-memory/adapters"
	"central-memory/internal/azure"
	"central-memory/internal/backup"
	"central-memory/internal/deps"
	"central-memory/internal/install"
	"central-memory/internal/project"
	"central-memory/internal/recall"
	"central-memory/internal/scan"
	"central-memory/internal/ui"
	"central-memory/internal/vault"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	vaultRoot := envOr("CENTRAL_MEMORY_VAULT", vault.DefaultPath())
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(vaultRoot)
	case "remember":
		err = cmdRemember(vaultRoot, os.Args[2:])
	case "recall":
		err = cmdRecall(vaultRoot, os.Args[2:])
	case "sync":
		err = cmdSync(vaultRoot)
	case "status":
		err = cmdStatus(vaultRoot)
	case "doctor":
		err = cmdDoctor(vaultRoot)
	case "harvest":
		err = cmdHarvest(vaultRoot, os.Args[2:])
	case "sessions":
		err = cmdSessions(vaultRoot, os.Args[2:])
	case "projects":
		err = cmdProjects(os.Args[2:])
	case "restore":
		err = cmdRestore(vaultRoot, os.Args[2:])
	case "backup":
		err = cmdBackup(vaultRoot, os.Args[2:])
	case "ui":
		err = cmdUI(vaultRoot, os.Args[2:])
	case "login":
		err = cmdLogin(vaultRoot, os.Args[2:])
	case "remote":
		err = cmdRemote(vaultRoot, os.Args[2:])
	case "install":
		err = cmdInstall(vaultRoot, os.Args[2:])
	case "uninstall":
		err = cmdUninstall(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`mem — central memory (P3, Go)
  mem init [--remote URL]                 scaffold vault + git
  mem remember -p <global|proj> -t tags "2-3 line fact"
  mem recall "query" [-p proj]            top 12 entries, ~1000 tokens max
  mem sync                                secret scan + git add/commit/pull --rebase/push
  mem status | mem doctor
  mem harvest [--dry-run] [--agent NAME]   export adapters to vault + normalize
  mem sessions [--project X] [--agent Y]  list normalized sessions, grouped
  mem projects                          list detected leaf projects on D:\
  mem restore [--project X] [--agent Y] [--at TIME] [--apps]  restore all agents for project
  mem backup [--projects] [--apps]        local mirror (15min) + apps export; remote hourly stub
  mem ui [--port 8321]                    local dashboard (localhost only)
  mem login azure [flags]                 az login + save settings (no secrets stored)
  mem remote push|status                  15-min restic->Azure Hot leg
  mem install [--dry-run] [--with-deps] | mem uninstall [--dry-run]`)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func cmdInit(root string) error {
	remote := ""
	for i, a := range os.Args {
		if a == "--remote" && i+1 < len(os.Args) {
			remote = os.Args[i+1]
		}
	}
	if err := vault.Layout(root); err != nil {
		return err
	}
	// Vault-level gitignore: NEVER list must never reach GitHub.
	// Raw agent DBs + local drive mirror travel via restic->Azure, not git.
	gi := "node_modules/\ndist/\nbuild/\n.next/\n__pycache__/\n.venv/\n*.pem\n*.key\n.env*\ncredentials.json\nauth.json\nagents/*/raw/\ndrive-mirror/\n*.pre-restore-*\n"
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(gi), 0o644); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); os.IsNotExist(err) {
		if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %v: %s", err, out)
		}
	}
	// Vault-local identity so commits never depend on global git config.
	for _, kv := range [][2]string{{"user.name", "central-memory"}, {"user.email", "mem@localhost"}} {
		if out, err := exec.Command("git", "-C", root, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			return fmt.Errorf("git config %s: %v: %s", kv[0], err, out)
		}
	}
	if remote != "" {
		_ = exec.Command("git", "-C", root, "remote", "add", "origin", remote).Run()
	}
	fmt.Println("vault ready at", root)
	fmt.Println("cadence:", scan.Cadence)
	return nil
}

func cmdRemember(root string, args []string) error {
	project, tags, text := "global", "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--project":
			if i+1 < len(args) {
				project = args[i+1]
				i++
			}
		case "-t", "--tags":
			if i+1 < len(args) {
				tags = args[i+1]
				i++
			}
		default:
			if text == "" {
				text = args[i]
			} else {
				text += " " + args[i]
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("usage: mem remember -p <global|proj> -t tags \"2-3 line fact\"")
	}
	// Summarize-on-write discipline: keep entries atomic, not transcripts.
	if len(text) > 600 {
		return fmt.Errorf("entry too long (%d chars); compress to 2-3 lines, raw transcripts belong in normalized archive (P2)", len(text))
	}
	var path string
	if project == "global" {
		path = filepath.Join(root, "memory", "global", "learnings.md")
	} else {
		dir := filepath.Join(root, "memory", "projects", project)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		path = filepath.Join(dir, "MEMORY.md")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line := fmt.Sprintf("\n## %s\ntags: %s\n\n%s\n", time.Now().UTC().Format("2006-01-02 15:04"), tags, strings.TrimSpace(text))
	if _, err := f.WriteString(line); err != nil {
		return err
	}
	return rebuildIndex(root)
}

func rebuildIndex(root string) error {
	type row struct {
		Path string `json:"path"`
	}
	var rows []row
	_ = filepath.Walk(filepath.Join(root, "memory"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".md") {
			rows = append(rows, row{Path: p})
		}
		return nil
	})
	b, _ := json.MarshalIndent(rows, "", "  ")
	return os.WriteFile(filepath.Join(root, ".index.jsonl"), b, 0o644)
}

func cmdRecall(root string, args []string) error {
	query, project := "", ""
	for i := 0; i < len(args); i++ {
		if (args[i] == "-p" || args[i] == "--project") && i+1 < len(args) {
			project = args[i+1]
			i++
		} else if query == "" {
			query = args[i]
		} else {
			query += " " + args[i]
		}
	}
	if query == "" {
		return fmt.Errorf("usage: mem recall \"query\" [-p proj]")
	}
	base := filepath.Join(root, "memory")
	if project != "" && project != "global" {
		base = filepath.Join(root, "memory", "projects", project)
	}
	type hit struct {
		file  string
		chunk string
		score int
	}
	var hits []hit
	terms := strings.Fields(strings.ToLower(query))
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, blk := range strings.Split(string(data), "\n## ") {
			low := strings.ToLower(blk)
			s := 0
			for _, t := range terms {
				s += strings.Count(low, t)
			}
			if s > 0 {
				hits = append(hits, hit{file: p, chunk: "## " + strings.TrimSpace(blk), score: s})
			}
		}
		return nil
	})
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	budget, out := recall.MaxChars, 0
	for i, h := range hits {
		if i >= recall.MaxEntries || out >= budget {
			break
		}
		fmt.Printf("--- %s (score %d)\n%s\n\n", h.file, h.score, truncate(h.chunk, 800))
		out += len(h.chunk)
	}
	if len(hits) == 0 {
		fmt.Println("no matches")
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func secretScan(root string) error {
	var bad []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.Contains(p, ".git"+string(filepath.Separator)) {
			return nil
		}
		// Raw agent DBs + local drive mirror go via restic->Azure, never GitHub.
		if scan.ShouldSkip(p) {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || len(data) > 2<<20 {
			return nil
		}
		for _, re := range scan.NeverPatterns {
			if re.Match(data) {
				bad = append(bad, p)
				break
			}
		}
		return nil
	})
	if len(bad) > 0 {
		return fmt.Errorf("secret scan blocked sync (fail closed): %s", strings.Join(bad, ", "))
	}
	return nil
}

func cmdSync(root string) error {
	if err := secretScan(root); err != nil {
		return err
	}
	run := func(args ...string) string {
		out, _ := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		return string(out)
	}
	fmt.Print(run("add", "-A"))
	status := run("status", "--porcelain")
	if strings.TrimSpace(status) == "" {
		fmt.Println("clean — nothing to push")
		return nil
	}
	msg := "sync " + time.Now().UTC().Format("2006-01-02 15:04")
	if out, err := exec.Command("git", "-C", root, "commit", "-m", msg).CombinedOutput(); err != nil {
		return fmt.Errorf("git commit failed: %v: %s", err, strings.TrimSpace(string(out)))
	} else {
		fmt.Print(string(out))
	}
	fmt.Print(run("pull", "--rebase"))
	fmt.Print(run("push"))
	return nil
}

func cmdStatus(root string) error {
	out, _ := exec.Command("git", "-C", root, "status", "--porcelain").CombinedOutput()
	dirty := strings.Count(strings.TrimSpace(string(out)), "\n")
	if strings.TrimSpace(string(out)) == "" {
		dirty = 0
	}
	fmt.Printf("vault: %s\ngit (GitHub leg): %d dirty file(s) — raw/ + drive-mirror/ excluded by design\n", root, dirty)
	fmt.Println("agents (per-adapter export state):")
	for _, ad := range adapters.Registry() {
		adir := filepath.Join(root, "agents", ad.Name())
		raw := countFiles(filepath.Join(adir, "raw"))
		sess := countLines(filepath.Join(adir, "normalized", "sessions.jsonl"))
		idx := "no index (run harvest)"
		if _, err := os.Stat(filepath.Join(adir, "index.json")); err == nil {
			idx = "indexed"
		}
		fmt.Printf("  %-8s raw=%d sessions=%d %s\n", ad.Name(), raw, sess, idx)
	}
	if data, err := os.ReadFile(filepath.Join(root, "drive-mirror", "last-run.json")); err == nil {
		fmt.Printf("drive-mirror (local leg): %s\n", strings.TrimSpace(string(data)))
	} else {
		fmt.Println("drive-mirror (local leg): never run (mem backup --projects)")
	}
	if ok, detail := azure.Status(root); ok {
		fmt.Println("remote (Azure leg):", detail)
	} else {
		fmt.Println("remote (Azure leg):", detail)
	}
	return nil
}

func countFiles(dir string) int {
	n := 0
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func countLines(p string) int {
	data, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

func cmdDoctor(root string) error {
	check := func(name string, ok bool) {
		mark := "OK  "
		if !ok {
			mark = "FAIL"
		}
		fmt.Printf("[%s] %s\n", mark, name)
	}
	_, errGit := exec.LookPath("git")
	_, errVault := os.Stat(filepath.Join(root, "manifest.json"))
	_, errD := os.Stat(`D:\`)
	fmt.Println("mem doctor — tools + vault + D: + adapters")
	check("git installed", errGit == nil)
	fmt.Println("== dependencies ==")
	fmt.Print(deps.Summary())
	fmt.Println("== vault ==")
	check("vault initialized", errVault == nil)
	check("D: present", errD == nil)
	// Native agent dirs detected.
	for _, ad := range adapters.Registry() {
		arts, _ := ad.Discover()
		fmt.Printf("  %-12s %d files\n", ad.Name(), len(arts))
	}
	_ = bufio.NewReader(os.Stdin)
	return nil
}

func cmdProjects(args []string) error {
	for _, leaf := range project.Leaves() {
		origin, root := project.Fingerprint(filepath.Join(`D:\`, filepath.FromSlash(leaf)))
		id := leaf
		if origin != "" {
			id += "  [" + origin + "]"
		} else if root != "" {
			id += "  [root " + root[:12] + "]"
		}
		fmt.Println(id)
		_ = args
	}
	return nil
}

// ---- P2: harvest / sessions / restore ----

func cmdHarvest(root string, args []string) error {
	dry, only := false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dry = true
		case "--agent":
			if i+1 < len(args) {
				only = args[i+1]
				i++
			}
		}
	}
	total := 0
	for _, ad := range adapters.Registry() {
		if only != "" && ad.Name() != only {
			continue
		}
		arts, _ := ad.Discover()
		fmt.Printf("[%s] %d backup-eligible files\n", ad.Name(), len(arts))
		total += len(arts)
		if dry {
			continue
		}
		if err := ad.Export(root); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] export: %v\n", ad.Name(), err)
			continue
		}
		if err := ad.Normalize(root); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] normalize: %v\n", ad.Name(), err)
		}
	}
	fmt.Printf("total %d files%s\n", total, map[bool]string{true: " (dry-run, nothing written)", false: ""}[dry])
	return nil
}

func cmdSessions(root string, args []string) error {
	project, agent := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 < len(args) {
				project = args[i+1]
				i++
			}
		case "--agent":
			if i+1 < len(args) {
				agent = args[i+1]
				i++
			}
		}
	}
	// Grouped by project, all agents by default: sessions --project X shows
	// claude+cursor+opencode+codex slices together.
	byProject := map[string][]string{}
	_ = filepath.Walk(filepath.Join(root, "agents"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Base(p) != "sessions.jsonl" {
			return nil
		}
		ag := filepath.Base(filepath.Dir(filepath.Dir(p)))
		if agent != "" && ag != agent {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) != nil {
				continue
			}
			proj, _ := row["project"].(string)
			if project != "" && proj != project {
				continue
			}
			sid, _ := row["session_id"].(string)
			upd, _ := row["updated"].(string)
			if was, _ := row["was"].(string); was != "" {
				sid += "  [moved from " + was + "]"
			}
			byProject[proj] = append(byProject[proj], fmt.Sprintf("  [%s] %s %s", ag, upd, sid))
		}
		return nil
	})
	if len(byProject) == 0 {
		fmt.Println("no sessions indexed yet (run: mem harvest)")
		return nil
	}
	projs := make([]string, 0, len(byProject))
	for k := range byProject {
		projs = append(projs, k)
	}
	sort.Strings(projs)
	for _, pr := range projs {
		fmt.Println(pr)
		rows := byProject[pr]
		sort.Strings(rows)
		n := len(rows)
		if n > 20 {
			rows = rows[n-20:]
		}
		for _, r := range rows {
			fmt.Println(r)
		}
		fmt.Println()
	}
	return nil
}

func cmdRestore(root string, args []string) error {
	project, agent, at := "", "", ""
	wantApps := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 < len(args) {
				project = args[i+1]
				i++
			}
		case "--agent":
			if i+1 < len(args) {
				agent = args[i+1]
				i++
			}
		case "--at":
			if i+1 < len(args) {
				at = args[i+1]
				i++
			}
		case "--apps":
			wantApps = true
		}
	}
	if wantApps {
		ps := filepath.Join(root, "system", "restore.ps1")
		if _, err := os.Stat(ps); err != nil {
			return fmt.Errorf("no apps export yet (run: mem backup --apps)")
		}
		fmt.Println("apps restore plan (generated):")
		fmt.Println("  1. winget import " + filepath.Join(root, "system", "winget.json"))
		fmt.Println("  2. reinstall vscode extensions from system/vscode-extensions.txt")
		fmt.Println("  3. copy dotfiles from system/ to your home dir")
		fmt.Println("run: powershell -ExecutionPolicy Bypass -File " + ps)
		return nil
	}
	// Default: all agents for the project (or everything if no filter).
	for _, ad := range adapters.Registry() {
		if agent != "" && ad.Name() != agent {
			continue
		}
		if err := ad.Restore(root, project, at); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] restore: %v\n", ad.Name(), err)
		}
	}
	return nil
}

// ---- P3: backup / restore --apps / ui ----

func cmdBackup(root string, args []string) error {
	wantProjects, wantApps := false, false
	for _, a := range args {
		switch a {
		case "--projects":
			wantProjects = true
		case "--apps":
			wantApps = true
		}
	}
	if !wantProjects && !wantApps {
		wantProjects, wantApps = true, true
	}
	if wantProjects {
		fmt.Println("== drive mirror (local, 15-min cadence) ==")
		report, err := backup.MirrorProjects(root)
		fmt.Println(report)
		if err != nil {
			fmt.Fprintln(os.Stderr, "mirror:", err)
		}
	}
	if wantApps {
		fmt.Println("== apps export ==")
		report, err := backup.ExportApps(root)
		fmt.Println(report)
		if err != nil {
			fmt.Fprintln(os.Stderr, "apps:", err)
		}
	}
	ok, detail := azure.Status(root)
	fmt.Println("== remote (15-min Azure Hot) ==")
	fmt.Println(detail)
	if !ok {
		fmt.Println("local backup complete; off-device push pending creds (see detail above)")
	}
	return nil
}

func cmdUI(root string, args []string) error {
	port := "8321"
	for i, a := range args {
		if a == "--port" && i+1 < len(args) {
			port = args[i+1]
		}
	}
	return ui.Serve(root, port)
}

// ---- Azure remote + install ----

func cmdLogin(root string, args []string) error {
	if len(args) == 0 || args[0] != "azure" {
		return fmt.Errorf("usage: mem login azure [--subscription S --group G --storage NAME --container C --vault-name VAULT]")
	}
	return azure.Login(root, args[1:])
}

func cmdRemote(root string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mem remote push|status")
	}
	switch args[0] {
	case "status":
		ok, detail := azure.Status(root)
		fmt.Println(detail)
		if !ok {
			return fmt.Errorf("remote not ready")
		}
		return nil
	case "push":
		return azure.Push(root)
	default:
		return fmt.Errorf("usage: mem remote push|status")
	}
}

func dryFlag(args []string) bool {
	for _, a := range args {
		if a == "--dry-run" {
			return true
		}
	}
	return false
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func cmdInstall(root string, args []string) error {
	return install.Install(root, dryFlag(args), hasFlag(args, "--with-deps"))
}

func cmdUninstall(args []string) error {
	return install.Uninstall(dryFlag(args))
}
