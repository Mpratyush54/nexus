package desktopui

import (
	"fmt"
	"path/filepath"
	"strings"

	"central-memory/internal/cloudclient"
	"central-memory/internal/localclient"
)

// section is the primary nav target when signed in.
type section int

const (
	secHome section = iota
	secConnect
	secMemory
	secHarvest
	secWorkspace
	secSettings
)

// shellMode is the top-level window mode.
type shellMode int

const (
	modeWelcome shellMode = iota
	modeApp
)

// harvestRow is one Harvest list entry (agent / transcript file / event).
type harvestRow struct {
	kind     string // agent, file, event
	title    string
	subtitle string
	filePath string
	detail   string
	agent    string
	format   string
}

// workspaceRow is one Workspace list entry (repo root / path — not harvest).
type workspaceRow struct {
	title string
	path  string
}

// connectItem is one Connect checklist row.
type connectItem struct {
	done   bool
	title  string
	detail string
	action string // optional CTA label
	goTo   section
	signIn bool
	pickWS bool
}

// deriveMode returns Welcome until a cloud token exists.
func deriveMode(signedIn bool) shellMode {
	if signedIn {
		return modeApp
	}
	return modeWelcome
}

func buildStatusLine(st *localclient.Status, signedIn bool) string {
	if st == nil {
		if !signedIn {
			return "Sign in to get started"
		}
		return "Daemon offline — local harvest and file preview need nexus-daemon on :7272"
	}
	switch {
	case strings.TrimSpace(st.Root) == "":
		return "No workspace folder — choose one on Home or Workspace"
	case st.Connected:
		return "Connected · " + first(st.Username, st.UserID)
	case st.HasToken:
		return "Signed in — linking workspace…"
	case signedIn:
		return "Signed in — starting local daemon…"
	default:
		return "Sign in to sync memory and harvest"
	}
}

func buildHomeMarkdown(st *localclient.Status, h *localclient.Harvest, signedIn bool) string {
	var b strings.Builder
	b.WriteString("### Home\n\n")
	if !signedIn {
		b.WriteString("You are not signed in. Open **Welcome** / use **Sign in** to continue.\n")
		return b.String()
	}
	b.WriteString("#### Account\n")
	if st != nil {
		b.WriteString(fmt.Sprintf("- **User:** %s\n", orDash(first(st.Username, st.UserID))))
		b.WriteString(fmt.Sprintf("- **Server:** %s\n", orDash(st.ServerURL)))
	} else {
		b.WriteString("- Daemon offline — status unavailable\n")
	}
	b.WriteString("\n#### Workspace\n")
	if st != nil && strings.TrimSpace(st.Root) != "" {
		b.WriteString(fmt.Sprintf("- **Folder:** %s\n", st.Root))
		b.WriteString(fmt.Sprintf("- **Linked id:** %s\n", orDash(st.WorkspaceID)))
	} else {
		b.WriteString("- No folder yet — use **Home** or **Workspace** to pick one.\n")
	}
	b.WriteString("\n#### Harvest\n")
	if h != nil {
		b.WriteString(fmt.Sprintf("- Last scan %s · files/turns %d/%d\n",
			orDash(h.LastScanAt), h.LastScanFiles, h.LastScanTurns))
		b.WriteString(fmt.Sprintf("- Active sessions %d · saved/errors %d/%d\n",
			h.ActiveSessions, h.ProposalsSaved, h.ProposalErrors))
		if msg := strings.TrimSpace(h.Message); msg != "" {
			b.WriteString("\n" + msg + "\n")
		}
	} else {
		b.WriteString("- Harvest data unavailable until the daemon is online.\n")
	}
	b.WriteString("\n**Memory** = cloud facts · **Harvest** = agent transcripts · **Workspace** = repo files.\n")
	return b.String()
}

func buildConnectChecklist(st *localclient.Status, h *localclient.Harvest, signedIn bool) []connectItem {
	items := make([]connectItem, 0, 4)

	items = append(items, connectItem{
		done:   signedIn,
		title:  "Sign in",
		detail: ternary(signedIn, "Cloud account ready", "Open the system browser to authenticate"),
		action: ternary(signedIn, "", "Sign in"),
		signIn: !signedIn,
	})

	hasRoot := st != nil && strings.TrimSpace(st.Root) != ""
	rootDetail := "Pick a repo root to watch (Fyne folder dialog)"
	if hasRoot {
		rootDetail = st.Root
	}
	items = append(items, connectItem{
		done:   hasRoot,
		title:  "Choose workspace folder",
		detail: rootDetail,
		action: ternary(hasRoot, "Change…", "Choose folder…"),
		pickWS: true,
		goTo:   secWorkspace,
	})

	daemonOK := st != nil
	daemonDetail := "Start after sign-in + workspace, or Refresh on Home"
	if daemonOK {
		daemonDetail = "Talking to nexus-daemon on :7272"
	}
	items = append(items, connectItem{
		done:   daemonOK,
		title:  "Local daemon online",
		detail: daemonDetail,
	})

	harvestReady := h != nil && (h.LastScanFiles > 0 || len(h.Agents) > 0 || len(h.Files) > 0 || strings.TrimSpace(h.LastScanAt) != "")
	harvestDetail := "Run Scan on Home or from the tray after the daemon is up"
	if harvestReady {
		harvestDetail = "Agent transcript scan available"
	}
	items = append(items, connectItem{
		done:   harvestReady,
		title:  "Harvest ready",
		detail: harvestDetail,
		goTo:   secHarvest,
	})
	return items
}

func buildHarvestRows(h *localclient.Harvest) []harvestRow {
	if h == nil {
		return nil
	}
	var rows []harvestRow
	for _, a := range h.Agents {
		name := first(a.Name, a.Agent, "agent")
		files := a.FilesSeen
		if files == 0 {
			files = a.FileCount
		}
		format := first(a.Format, a.Kind, "—")
		nForAgent := 0
		for _, f := range h.Files {
			if strings.EqualFold(first(f.Agent, ""), name) {
				nForAgent++
			}
		}
		detail := fmt.Sprintf("Harness %s (%s).\n%d transcript file(s) in latest scan list · %d files seen overall.\n\nSelect this row to preview the newest transcript, or pick a file row below.",
			name, format, nForAgent, files)
		rows = append(rows, harvestRow{
			kind:     "agent",
			title:    name,
			subtitle: fmt.Sprintf("%s · %d listed · %d seen", format, nForAgent, files),
			detail:   detail,
			agent:    name,
			format:   format,
		})
	}
	for _, f := range h.Files {
		label := first(f.Name, filepath.Base(f.Path), "transcript")
		rows = append(rows, harvestRow{
			kind:     "file",
			title:    label,
			subtitle: first(f.Agent, "agent") + " · " + first(f.Format, "format"),
			filePath: f.Path,
			agent:    first(f.Agent, ""),
			format:   first(f.Format, ""),
		})
	}
	for _, ev := range h.Recent {
		title := first(ev.Type, ev.Event, "event")
		detail := first(ev.Message, ev.Detail)
		if t := first(ev.At, ev.Time); t != "" {
			detail = t + "\n" + detail
		}
		rows = append(rows, harvestRow{
			kind:     "event",
			title:    title,
			subtitle: truncate(detail, 80),
			detail:   detail,
		})
	}
	return rows
}

// harvestFilesForAgent returns scan-listed transcript files for one harness.
func harvestFilesForAgent(h *localclient.Harvest, agent string) []localclient.HarvestFileHit {
	if h == nil || strings.TrimSpace(agent) == "" {
		return nil
	}
	var out []localclient.HarvestFileHit
	for _, f := range h.Files {
		if strings.EqualFold(f.Agent, agent) {
			out = append(out, f)
		}
	}
	return out
}

// buildWorkspaceRows lists workspace root / git metadata only — never harvest transcripts.
func buildWorkspaceRows(st *localclient.Status, ws *localclient.Workspace) []workspaceRow {
	var rows []workspaceRow
	if ws != nil && ws.Path != "" {
		rows = append(rows, workspaceRow{
			title: first(ws.Project, filepath.Base(ws.Path)) + " (root)",
			path:  ".",
		})
		if ws.Branch != "" {
			rows = append(rows, workspaceRow{
				title: fmt.Sprintf("Git branch %s @ %s", ws.Branch, truncate(ws.Commit, 8)),
				path:  "",
			})
		}
	} else if st != nil && strings.TrimSpace(st.Root) != "" {
		rows = append(rows, workspaceRow{
			title: filepath.Base(st.Root) + " (root)",
			path:  ".",
		})
	}
	if st != nil && st.Root != "" {
		rows = append(rows, workspaceRow{
			title: "Path: " + st.Root,
			path:  "",
		})
	}
	return rows
}

func formatMemoryPreview(it cloudclient.MemoryItem) string {
	return formatMemoryDetail(it)
}

// memoryProjectID returns the cloud project UUID for memory APIs.
// Never use Status.WorkspaceID — that is a workspace row id, not a project.
func memoryProjectID(st *localclient.Status, h *localclient.Harvest) string {
	if h != nil {
		if pid := strings.TrimSpace(h.ProjectID); looksLikeProjectUUID(pid) {
			return pid
		}
	}
	_ = st // reserved: do not fall back to WorkspaceID
	return ""
}

func looksLikeProjectUUID(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 36 {
		return false
	}
	// xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
				return false
			}
		}
	}
	return true
}

func pidHint(projectID string) string {
	if projectID == "" {
		return "\n\nTip: finish setup on Home (workspace + harvest) so a project is linked."
	}
	return "\n\nProject: " + projectID
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func first(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func recentDisplayName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return path
	}
	return base + " — " + path
}
