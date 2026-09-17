// Package ui serves the local dashboard: full terminal parity (status,
// projects, memory, sessions, harvest, restore, backup, remote, doctor).
// Stdlib only. Binds localhost — never 0.0.0.0. Long actions (harvest,
// backup, restore, remote push) run as background jobs with a live log page.
package ui

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"central-memory/internal/azure"
)

var tmpl = template.Must(template.New("page").Parse(`<!doctype html><html lang=en><head><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><title>mem · {{.Title}}</title>
<style>
:root{--bg:#0d1117;--panel:#161b22;--panel2:#1c2330;--line:#2d333b;--txt:#e6edf3;--mut:#8b949e;--acc:#4c9aff;--ok:#3fb950;--warn:#d29922;--bad:#f85149}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--txt);font:15px/1.55 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
.layout{display:flex;min-height:100vh}
aside{width:220px;flex-shrink:0;background:var(--panel);border-right:1px solid var(--line);padding:20px 0;position:sticky;top:0;height:100vh}
.logo{font-weight:800;font-size:17px;padding:0 20px 16px}.logo span{color:var(--acc);font-weight:400;font-size:12px;display:block}
aside nav a{display:block;color:var(--mut);text-decoration:none;padding:8px 20px;font-weight:500;border-left:3px solid transparent}
aside nav a:hover{color:var(--txt);background:var(--panel2)}
aside nav a.on{color:var(--txt);border-left-color:var(--acc);background:var(--panel2)}
main{flex:1;min-width:0;padding:8px 28px 60px;max-width:1000px}
h1{font-size:22px;margin:22px 0 4px}.sub{color:var(--mut);font-size:13px;margin:0 0 16px}
.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:12px;margin:14px 0}
.card{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:16px}
.card h3{margin:0 0 6px;font-size:14px}.card p{color:var(--mut);font-size:13px;margin:0 0 12px}
.stat .n{font-size:26px;font-weight:800}.stat .l{color:var(--mut);font-size:12px;text-transform:uppercase;letter-spacing:.05em}
button,.btn{background:var(--acc);color:#fff;border:0;border-radius:7px;padding:8px 16px;font-size:14px;font-weight:600;cursor:pointer;text-decoration:none;display:inline-block}
button:hover,.btn:hover{filter:brightness(1.12)}button.ghost{background:var(--panel2);border:1px solid var(--line)}
code{background:#0a0e12;border:1px solid var(--line);border-radius:5px;padding:1px 6px;font-size:13px}
pre.log{background:#0a0e12;border:1px solid var(--line);border-radius:8px;padding:12px;overflow:auto;font-size:13px;white-space:pre-wrap}
table{width:100%;border-collapse:collapse;font-size:14px}th,td{padding:8px 10px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}
th{color:var(--mut);font-weight:600;font-size:12px;text-transform:uppercase;letter-spacing:.04em}
tr:hover td{background:rgba(76,154,255,.05)}
.badge{display:inline-block;font-size:12px;font-weight:700;border-radius:20px;padding:2px 10px;white-space:nowrap}
.ok{background:rgba(63,185,80,.15);color:var(--ok)}.miss{background:rgba(210,153,34,.15);color:var(--warn)}.bad{background:rgba(248,81,73,.15);color:var(--bad)}
form.rowf{display:flex;gap:8px;margin:12px 0;flex-wrap:wrap}input,select{background:#0a0e12;border:1px solid var(--line);color:var(--txt);border-radius:7px;padding:8px 12px;font-size:14px}
form.rowf input{flex:1;min-width:140px}
.kv{display:flex;gap:10px;align-items:baseline;padding:7px 0;border-bottom:1px dashed var(--line);flex-wrap:wrap}
.kv b{min-width:150px;color:var(--mut);font-weight:500}
.hint{color:var(--mut);font-size:13px}
.entry{margin:0 0 12px}
details.card{margin-bottom:10px}details.card summary{cursor:pointer;list-style:none}details.card summary::-webkit-details-marker{display:none}
details.card summary:hover{color:var(--acc)}
td a{color:var(--acc);text-decoration:none}td a:hover{text-decoration:underline}
label.f{display:block;margin:10px 0 4px;color:var(--mut);font-size:13px}
@media(max-width:760px){.layout{flex-direction:column}aside{width:100%;height:auto;position:static}aside nav a{display:inline-block;border:0;padding:6px 10px}}
</style></head><body><div class=layout>
<aside><div class=logo>mem<span>central memory</span></div><nav>{{.Nav}}</nav></aside>
<main>{{template "body" .}}</main></div></body></html>
{{define "body"}}{{.Body}}{{end}}`))

type page struct {
	Title string
	Nav   template.HTML
	Body  template.HTML
}

var links = [][2]string{
	{"/", "status"}, {"/projects", "projects"}, {"/memory", "memory"},
	{"/sessions", "sessions"}, {"/harvest", "harvest"}, {"/restore", "restore"},
	{"/backup", "backup"}, {"/remote", "remote"}, {"/doctor", "doctor"},
}

func nav(cur string) template.HTML {
	var b strings.Builder
	for _, l := range links {
		cls := ""
		if l[0] == cur {
			cls = ` class=on`
		}
		fmt.Fprintf(&b, `<a href="%s"%s>%s</a>`, l[0], cls, l[1])
	}
	return template.HTML(b.String())
}

func render(w http.ResponseWriter, cur, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, page{Title: title, Nav: nav(cur), Body: template.HTML(body)})
}

func esc(s string) string { return template.HTMLEscapeString(s) }

// ---------- background jobs ----------

type job struct {
	ID     string
	Name   string
	Done   bool
	Output string
	Err    string
}

var (
	jobsMu sync.Mutex
	jobs   = map[string]*job{}
	jobSeq int
)

func startJob(vault, name string, args []string) *job {
	jobsMu.Lock()
	jobSeq++
	id := fmt.Sprint(jobSeq)
	j := &job{ID: id, Name: name}
	jobs[id] = j
	jobsMu.Unlock()
	go func() {
		exe, err := os.Executable()
		var out []byte
		if err == nil {
			cmd := exec.Command(exe, args...)
			cmd.Env = append(os.Environ(), "CENTRAL_MEMORY_VAULT="+vault)
			out, err = cmd.CombinedOutput()
		}
		jobsMu.Lock()
		j.Output = string(out)
		if err != nil {
			j.Err = err.Error()
		}
		j.Done = true
		jobsMu.Unlock()
	}()
	return j
}

func getJob(id string) *job {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	return jobs[id]
}

func resultPage(cur, title, output, errStr, back string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<h1>%s</h1>", esc(title))
	if errStr != "" {
		b.WriteString(`<p><span class="badge bad">failed</span> <code>` + esc(errStr) + `</code></p>`)
	} else {
		b.WriteString(`<p><span class="badge ok">done</span></p>`)
	}
	b.WriteString(`<pre class=log>` + esc(output) + `</pre>`)
	fmt.Fprintf(&b, `<p><a class=btn href="%s">back</a></p>`, back)
	return b.String()
}

// ---------- server ----------

// Serve starts the dashboard on 127.0.0.1:port (blocks).
func Serve(vault, port string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		render(w, "/", "status", statusHTML(vault))
	})
	mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		render(w, "/projects", "projects", projectsHTML(vault))
	})
	mux.HandleFunc("/memory", func(w http.ResponseWriter, r *http.Request) {
		render(w, "/memory", "memory", memoryHTML(vault, r.URL.Query().Get("q"), r.URL.Query().Get("p")))
	})
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		render(w, "/sessions", "sessions", sessionsHTML(vault, r.URL.Query().Get("project")))
	})
	mux.HandleFunc("/harvest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			j := startJob(vault, "harvest "+r.FormValue("agent"), []string{"harvest", "--agent", r.FormValue("agent")})
			if r.FormValue("agent") == "" {
				j = startJob(vault, "harvest all", []string{"harvest"})
			}
			http.Redirect(w, r, "/job?id="+j.ID, http.StatusSeeOther)
			return
		}
		render(w, "/harvest", "harvest", harvestHTML(vault))
	})
	mux.HandleFunc("/restore", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			args := []string{"restore"}
			if v := r.FormValue("project"); v != "" {
				args = append(args, "--project", v)
			}
			if v := r.FormValue("agent"); v != "" {
				args = append(args, "--agent", v)
			}
			if v := r.FormValue("at"); v != "" {
				args = append(args, "--at", v)
			}
			if r.FormValue("apps") != "" {
				args = []string{"restore", "--apps"}
			}
			j := startJob(vault, "restore", args)
			http.Redirect(w, r, "/job?id="+j.ID, http.StatusSeeOther)
			return
		}
		render(w, "/restore", "restore", restoreHTML(vault, r.URL.Query().Get("project")))
	})
	mux.HandleFunc("/backup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			j := startJob(vault, "backup", []string{"backup", "--projects", "--apps"})
			http.Redirect(w, r, "/job?id="+j.ID, http.StatusSeeOther)
			return
		}
		render(w, "/backup", "backup", backupHTML(vault))
	})
	mux.HandleFunc("/remote", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.FormValue("save") != "" {
				cur := azure.Load(vault)
				for k, f := range map[string]*string{"subscription": &cur.Subscription, "group": &cur.ResourceGroup, "storage": &cur.Storage, "container": &cur.Container, "keyvault": &cur.KeyVault} {
					if v := r.FormValue(k); v != "" {
						*f = v
					}
				}
				if err := azure.Save(vault, cur); err != nil {
					render(w, "/remote", "remote", resultPage("/remote", "save azure config", "", err.Error(), "/remote"))
					return
				}
				http.Redirect(w, r, "/remote", http.StatusSeeOther)
				return
			}
			j := startJob(vault, "remote push", []string{"remote", "push"})
			http.Redirect(w, r, "/job?id="+j.ID, http.StatusSeeOther)
			return
		}
		render(w, "/remote", "remote", remoteHTML(vault))
	})
	mux.HandleFunc("/doctor", func(w http.ResponseWriter, r *http.Request) {
		exe, _ := os.Executable()
		out, err := exec.Command(exe, "doctor").CombinedOutput()
		body := `<h1>doctor</h1><p class=sub>Environment + agent-directory health. Vault: <code>` + esc(vault) + `</code></p><pre class=log>` + esc(string(out))
		if err != nil {
			body += esc("\n"+err.Error())
		}
		body += `</pre>`
		render(w, "/doctor", "doctor", body)
	})
	mux.HandleFunc("/job", func(w http.ResponseWriter, r *http.Request) {
		j := getJob(r.URL.Query().Get("id"))
		if j == nil {
			http.NotFound(w, r)
			return
		}
		var b strings.Builder
		fmt.Fprintf(&b, "<h1>job: %s</h1>", esc(j.Name))
		if j.Done {
			if j.Err != "" {
				b.WriteString(`<p><span class="badge bad">failed</span> <code>` + esc(j.Err) + `</code></p>`)
			} else {
				b.WriteString(`<p><span class="badge ok">done</span></p>`)
			}
			b.WriteString(`<pre class=log>` + esc(j.Output) + `</pre>`)
		} else {
			b.WriteString(`<p><span class="badge miss">running…</span> auto-refreshing</p><pre class=log>` + esc(j.Output) + `</pre><meta http-equiv=refresh content=2>`)
		}
		render(w, "", "job "+j.ID, b.String())
	})
	// Legacy direct action endpoints (kept for bookmarks).
	mux.HandleFunc("/actions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/actions/")
		var args []string
		switch name {
		case "harvest":
			args = []string{"harvest"}
		case "sync":
			exe, _ := os.Executable()
			cmd := exec.Command(exe, "sync")
			cmd.Env = append(os.Environ(), "CENTRAL_MEMORY_VAULT="+vault)
			out, err := cmd.CombinedOutput()
			es := ""
			if err != nil {
				es = err.Error()
			}
			render(w, "", "sync", resultPage("", "sync", string(out), es, "/"))
			return
		case "backup":
			args = []string{"backup", "--projects", "--apps"}
		case "remote":
			args = []string{"remote", "push"}
		default:
			http.Error(w, "unknown action", 404)
			return
		}
		j := startJob(vault, name, args)
		http.Redirect(w, r, "/job?id="+j.ID, http.StatusSeeOther)
	})
	addr := "127.0.0.1:" + port
	fmt.Println("mem ui at http://" + addr)
	return http.ListenAndServe(addr, mux)
}
