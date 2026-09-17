package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"central-memory/internal/azure"
	"central-memory/internal/project"
)

// ---------- status ----------

func statusHTML(vault string) string {
	var b strings.Builder
	b.WriteString("<h1>status</h1><p class=sub>Vault health at a glance.</p>")
	agents, sessions := agentStats(vault)
	dirty := gitDirty(vault)
	mirror := "never run"
	if data, err := os.ReadFile(filepath.Join(vault, "drive-mirror", "last-run.json")); err == nil {
		mirror = strings.TrimSpace(string(data))
	}
	_, rdetail := azure.Status(vault)
	b.WriteString(`<div class=grid>`)
	fmt.Fprintf(&b, `<div class="card stat"><div class=n>%d / 14</div><div class=l>adapters indexed</div></div>`, agents)
	fmt.Fprintf(&b, `<div class="card stat"><div class=n>%s</div><div class=l>sessions archived</div></div>`, humanNum(sessions))
	ds := `<span class="badge ok">clean</span>`
	if dirty > 0 {
		ds = `<span class="badge miss">` + fmt.Sprint(dirty) + ` dirty</span>`
	}
	fmt.Fprintf(&b, `<div class="card stat"><div class=n>%s</div><div class=l>git (GitHub leg)</div></div>`, ds)
	b.WriteString(`</div><div class=card>`)
	b.WriteString(`<div class=kv><b>vault</b><code>` + esc(vault) + `</code></div>`)
	b.WriteString(`<div class=kv><b>drive mirror</b><span class=hint>` + esc(mirror) + `</span></div>`)
	b.WriteString(`<div class=kv><b>remote</b><span class=hint>` + esc(rdetail) + `</span></div>`)
	b.WriteString(`</div><div class=grid>
<div class=card><h3>harvest</h3><p>Export agent sessions + rebuild index.</p><form method=post action=/harvest><button>open harvest</button></form></div>
<div class=card><h3>restore</h3><p>Bring a project's sessions back, all agents.</p><form method=post action=/restore><button>open restore</button></form></div>
<div class=card><h3>sync</h3><p>Secret-scan + push memory to GitHub.</p><form method=post action=/actions/sync><button>run sync now</button></form></div>
</div>`)
	return b.String()
}

func humanNum(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

func gitDirty(vault string) int {
	out, err := exec.Command("git", "-C", vault, "status", "--porcelain").Output()
	if err != nil {
		return -1
	}
	if strings.TrimSpace(string(out)) == "" {
		return 0
	}
	return strings.Count(strings.TrimSpace(string(out)), "\n") + 1
}

// agentStats counts adapters with index.json + total normalized sessions.
func agentStats(vault string) (agents, sessions int) {
	_ = filepath.Walk(filepath.Join(vault, "agents"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		switch filepath.Base(p) {
		case "index.json":
			agents++
		case "sessions.jsonl":
			if data, err := os.ReadFile(p); err == nil {
				sessions += strings.Count(string(data), "\n")
			}
		}
		return nil
	})
	return agents, sessions
}

// ---------- memory ----------

func memoryHTML(vault, q, proj string) string {
	var b strings.Builder
	b.WriteString(`<h1>memory</h1><p class=sub>Atomic entries — retrieval, not dumps. Capped display.</p><form class=rowf><input name=q placeholder="search…" value="` + esc(q) + `"> <input name=p placeholder="project" value="` + esc(proj) + `" style="max-width:180px"> <button>search</button></form>`)
	base := filepath.Join(vault, "memory")
	if proj != "" && proj != "global" {
		base = filepath.Join(vault, "memory", "projects", proj)
	}
	shown := 0
	_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".md") || shown >= 30 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, blk := range strings.Split(string(data), "\n## ") {
			if shown >= 30 {
				break
			}
			if q != "" && !strings.Contains(strings.ToLower(blk), strings.ToLower(q)) {
				continue
			}
			snip := blk
			if len(snip) > 500 {
				snip = snip[:500] + "…"
			}
			b.WriteString(`<div class="entry card"><div class=hint><code>` + esc(p) + `</code></div><pre class=log>` + esc(snip) + `</pre></div>`)
			shown++
		}
		return nil
	})
	if shown == 0 {
		b.WriteString(`<p class=hint>no matches — try fewer words, or run harvest first.</p>`)
	}
	return b.String()
}

// ---------- sessions ----------

type sessRow struct{ proj, ag, upd, sid, was string }

func scanSessions(vault, project string) []sessRow {
	var rows []sessRow
	_ = filepath.Walk(filepath.Join(vault, "agents"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Base(p) != "sessions.jsonl" {
			return nil
		}
		ag := filepath.Base(filepath.Dir(filepath.Dir(p)))
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) != nil {
				continue
			}
			pr, _ := m["project"].(string)
			if project != "" && pr != project {
				continue
			}
			upd, _ := m["updated"].(string)
			sid, _ := m["session_id"].(string)
			was, _ := m["was"].(string)
			rows = append(rows, sessRow{pr, ag, upd, sid, was})
		}
		return nil
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].upd > rows[j].upd })
	if len(rows) > 500 {
		rows = rows[:500]
	}
	return rows
}

func sortedKeys(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sessionsHTML(vault, project string) string {
	var b strings.Builder
	b.WriteString(`<h1>sessions</h1><p class=sub>Every agent's sessions, grouped by project.</p><form class=rowf><input name=project placeholder="filter by project…" value="` + esc(project) + `"> <button>filter</button></form>`)
	rows := scanSessions(vault, project)
	if len(rows) == 0 {
		b.WriteString(`<p class=hint>no sessions indexed yet — run harvest first.</p>`)
		return b.String()
	}
	groups := map[string][]sessRow{}
	var order []string
	for _, r := range rows {
		if _, ok := groups[r.proj]; !ok {
			order = append(order, r.proj)
		}
		groups[r.proj] = append(groups[r.proj], r)
	}
	sort.Strings(order)
	fmt.Fprintf(&b, `<p class=hint>%d sessions across %d projects.</p>`, len(rows), len(order))
	for i, pr := range order {
		rs := groups[pr]
		agents := map[string]int{}
		for _, r := range rs {
			agents[r.ag]++
		}
		var parts []string
		for _, ag := range sortedKeys(agents) {
			parts = append(parts, ag+` `+fmt.Sprint(agents[ag]))
		}
		open := ""
		if i == 0 {
			open = " open"
		}
		b.WriteString(`<details` + open + ` class=card><summary><b>` + esc(pr) + `</b> <span class="badge ok">` + fmt.Sprint(len(rs)) + `</span> <span class=hint>` + esc(strings.Join(parts, " · ")) + `</span></summary>`)
		b.WriteString(`<table><tr><th>agent</th><th>updated</th><th>session</th></tr>`)
		for _, r := range rs {
			sid := esc(r.sid)
			if r.was != "" {
				sid += ` <span class="badge miss" title="original location: ` + esc(r.was) + `">moved</span>`
			}
			b.WriteString("<tr><td>" + esc(r.ag) + "</td><td>" + esc(r.upd) + "</td><td><code>" + sid + "</code></td></tr>")
		}
		b.WriteString(`</table></details>`)
	}
	return b.String()
}

// ---------- projects ----------

func projectsHTML(vault string) string {
	type proj struct {
		name     string
		memory   bool
		sessions int
		byAgent  map[string]int
	}
	projs := map[string]*proj{}
	get := func(name string) *proj {
		if p, ok := projs[name]; ok {
			return p
		}
		p := &proj{name: name, byAgent: map[string]int{}}
		projs[name] = p
		return p
	}
	if entries, err := os.ReadDir(filepath.Join(vault, "memory", "projects")); err == nil {
		for _, e := range entries {
			get(e.Name()).memory = true
		}
	}
	// Leaf projects: nested repos (gitlab-test/Campus-Navigator) are each
	// their own project, not the parent folder.
	for _, leaf := range project.CachedLeaves() {
		get(leaf)
	}
	for _, r := range scanSessions(vault, "") {
		p := get(r.proj)
		p.sessions++
		p.byAgent[r.ag]++
	}
	var names []string
	for n := range projs {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, `<h1>projects</h1><p class=sub>%d projects on D:\ — memory file + session coverage per agent.</p>`, len(names))
	b.WriteString(`<table><tr><th>project</th><th>memory</th><th>sessions</th><th>agents</th><th></th></tr>`)
	for _, n := range names {
		p := projs[n]
		mem := `<span class="badge miss">none</span>`
		if p.memory {
			mem = `<span class="badge ok">yes</span>`
		}
		var parts []string
		for _, ag := range sortedKeys(p.byAgent) {
			parts = append(parts, ag+` `+fmt.Sprint(p.byAgent[ag]))
		}
		if len(parts) == 0 {
			parts = []string{`<span class=hint>run harvest</span>`}
		}
		b.WriteString(`<tr><td><b>` + esc(n) + `</b></td><td>` + mem + `</td><td>` + fmt.Sprint(p.sessions) + `</td><td>` + strings.Join(parts, " · ") + `</td>`)
		b.WriteString(`<td><a href="/sessions?project=` + esc(n) + `">sessions</a> · <a href="/memory?p=` + esc(n) + `">memory</a> · <a href="/restore?project=` + esc(n) + `">restore</a></td></tr>`)
	}
	b.WriteString(`</table>`)
	return b.String()
}

// ---------- harvest ----------

func harvestHTML(vault string) string {
	var b strings.Builder
	b.WriteString(`<h1>harvest</h1><p class=sub>Export native agent state into the vault + rebuild normalized indexes. Long runs execute in the background with a live log.</p>`)
	b.WriteString(`<form method=post><input type=hidden name=agent value=""><button>harvest all adapters</button></form>`)
	b.WriteString(`<table style="margin-top:14px"><tr><th>agent</th><th>files</th><th>sessions</th><th>last run</th><th></th></tr>`)
	known := []string{"claude", "opencode", "cursor", "codex", "antigravity", "copilot", "codeium", "kimi", "windsurf", "gemini", "grok", "commandcode", "cagent", "zcode"}
	seen := map[string]bool{}
	for _, n := range known {
		seen[n] = true
	}
	// Only direct children of agents/ count — raw/ trees contain their own
	// manifest.json files (extension manifests, etc.) which must be ignored.
	if entries, err := os.ReadDir(filepath.Join(vault, "agents")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				seen[e.Name()] = true
			}
		}
	}
	var order []string
	for n := range seen {
		order = append(order, n)
	}
	sort.Strings(order)
	for _, n := range order {
		files, sess, at := "-", "-", "never"
		if data, err := os.ReadFile(filepath.Join(vault, "agents", n, "index.json")); err == nil {
			var idx struct {
				Files []any  `json:"files"`
				At    string `json:"at"`
			}
			if json.Unmarshal(data, &idx) == nil {
				files = fmt.Sprint(len(idx.Files))
				at = idx.At
			}
		}
		if data, err := os.ReadFile(filepath.Join(vault, "agents", n, "normalized", "sessions.jsonl")); err == nil {
			sess = fmt.Sprint(strings.Count(string(data), "\n"))
		}
		badge := `<span class="badge miss">pending</span>`
		if at != "never" {
			badge = `<span class="badge ok">indexed</span>`
		}
		fmt.Fprintf(&b, `<tr><td><b>%s</b> %s</td><td>%s</td><td>%s</td><td class=hint>%s</td><td><form method=post style="display:inline"><input type=hidden name=agent value="%s"><button class=ghost>run</button></form></td></tr>`,
			esc(n), badge, files, sess, esc(at), esc(n))
	}
	b.WriteString(`</table>`)
	return b.String()
}

// ---------- restore ----------

func restoreHTML(vault, preset string) string {
	_ = vault
	var b strings.Builder
	b.WriteString("<h1>restore</h1><p class=sub>Default restores <b>all agents</b> for the project (same <code>D:</code> path enforced, current files backed up first).</p>")
	b.WriteString("<form method=post><div class=card>")
	b.WriteString("<label class=f>project (empty = everything)</label>")
	b.WriteString("<input name=project placeholder=\"portfolio\" style=\"width:100%\" value=\"" + esc(preset) + "\">")
	b.WriteString("<label class=f>agent (empty = all agents)</label>")
	b.WriteString("<input name=agent placeholder=\"claude\" style=\"width:100%\">")
	b.WriteString("<label class=f>at (time travel lands fully in P3/restic; P2 holds latest)</label>")
	b.WriteString("<input name=at placeholder=\"2026-09-15 16:00\" style=\"width:100%\">")
	b.WriteString("<p><button>run restore</button></p></div></form>")
	b.WriteString("<div class=card style=\"margin-top:12px\"><h3>apps</h3><p>Reinstall recipes (winget, extensions, dotfiles).</p><form method=post><input type=hidden name=apps value=1><button class=ghost>restore apps plan</button></form></div>")
	return b.String()
}

// ---------- backup ----------

func backupHTML(vault string) string {
	var b strings.Builder
	b.WriteString("<h1>backup</h1><p class=sub>Local mirror every 15 min (free) + apps export. Remote push on the remote page.</p><div class=card>")
	for _, f := range []string{"drive-mirror/last-run.json", "system/restore.ps1", "system/winget.json"} {
		if _, err := os.Stat(filepath.Join(vault, f)); err == nil {
			b.WriteString(`<div class=kv><b><code>` + esc(f) + `</code></b><span class="badge ok">present</span></div>`)
		} else {
			b.WriteString(`<div class=kv><b><code>` + esc(f) + `</code></b><span class="badge miss">missing</span></div>`)
		}
	}
	b.WriteString(`</div><p><form method=post><button>run backup now (projects + apps)</button></form></p>`)
	return b.String()
}

// ---------- remote ----------

func remoteHTML(vault string) string {
	var b strings.Builder
	b.WriteString("<h1>remote</h1><p class=sub>Off-device leg: restic → Azure Blob Hot every 15 min (no retention penalty; 30-day lifecycle down to Cool).</p>")
	ok, detail := azure.Status(vault)
	if ok {
		b.WriteString(`<p><span class="badge ok">ready</span></p>`)
	} else {
		b.WriteString(`<p><span class="badge miss">pending</span></p>`)
	}
	b.WriteString(`<div class=card><div class=kv><b>status</b><span class=hint>` + esc(detail) + `</span></div></div>`)
	c := azure.Load(vault)
	b.WriteString(`<div class=card style="margin-top:12px"><h3>azure config</h3>
<p class=hint>Settings only — secrets stay in Key Vault / env. Same fields as <code>mem login azure</code>.</p>
<form method=post>
<div class=kv><b>subscription</b><input name=subscription placeholder="sub id or name" value="` + esc(c.Subscription) + `"></div>
<div class=kv><b>resource group</b><input name=group placeholder="rg-name" value="` + esc(c.ResourceGroup) + `"></div>
<div class=kv><b>storage account</b><input name=storage placeholder="mystorageacct" value="` + esc(c.Storage) + `"></div>
<div class=kv><b>container</b><input name=container placeholder="central-backup" value="` + esc(c.Container) + `"></div>
<div class=kv><b>key vault</b><input name=keyvault placeholder="my-keyvault" value="` + esc(c.KeyVault) + `"></div>
<p><button name=save value=1>save azure config</button></p></form></div>`)
	b.WriteString(`<p><form method=post><button>push to azure now</button></form></p>`)
	return b.String()
}
