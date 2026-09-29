// Package desktopui is the native Fyne shell for Nexus Desktop (Win/Mac/Linux).
// Cockpit layout: portal-aligned sidebar, section content, VS Code-style preview.
package desktopui

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"central-memory/internal/buildinfo"
	"central-memory/internal/cloudclient"
	"central-memory/internal/localclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Hooks lets cmd/nexus-desktop wire sign-in, workspace pick, and updates.
type Hooks struct {
	OnSignIn         func()
	OnSignOut        func()
	OnOpenWebPortal  func() // explicit external: team/org/billing only
	OnPickFolder     func()
	OnCheckUpdate    func()
	OnQuit           func()
	UpdateLabel      func() string
	EnsureDaemon     func() error
}

type section int

const (
	secHome section = iota
	secMemory
	secHarvest
	secWorkspace
	secSettings
)

// Shell owns the main window content and a refresh loop.
type Shell struct {
	win    fyne.Window
	client *localclient.Client
	cloud  *cloudclient.Client
	hooks  Hooks

	section section

	statusLine  *widget.Label
	center      *fyne.Container
	previewHead *widget.Label
	previewBody *widget.Entry

	homeStats *widget.RichText

	memorySearch *widget.Entry
	memoryList   *widget.List
	memoryItems  []cloudclient.MemoryItem

	harvestList  *widget.List
	harvestRows  []harvestRow
	workspaceList *widget.List
	workspaceRows []workspaceRow

	settingsBox fyne.CanvasObject

	mu     sync.Mutex
	stopCh chan struct{}

	cachedStatus   *localclient.Status
	cachedHarvest  *localclient.Harvest
	cachedWorkspace *localclient.Workspace
}

type harvestRow struct {
	kind     string // agent, file, event
	title    string
	subtitle string
	filePath string
	detail   string
}

type workspaceRow struct {
	title string
	path  string
}

// NewShell builds the native window content. Call AttachRefresh after Show.
func NewShell(win fyne.Window, client *localclient.Client, hooks Hooks) *Shell {
	if client == nil {
		client = localclient.New("")
	}
	s := &Shell{
		win:    win,
		client: client,
		cloud:  cloudclient.New("", ""),
		hooks:  hooks,
		stopCh: make(chan struct{}),
	}
	win.SetTitle("Nexus")
	if app := fyne.CurrentApp(); app != nil {
		app.Settings().SetTheme(newNexusTheme())
	}

	s.statusLine = widget.NewLabel("Checking connection…")
	s.statusLine.TextStyle = fyne.TextStyle{Bold: true}

	s.previewHead = widget.NewLabel("Preview")
	s.previewHead.TextStyle = fyne.TextStyle{Bold: true}
	s.previewBody = widget.NewMultiLineEntry()
	s.previewBody.SetPlaceHolder("Select a memory entry or agent transcript file to inspect content here.")
	s.previewBody.Wrapping = fyne.TextWrapWord
	s.previewBody.Disable()

	s.homeStats = widget.NewRichTextFromMarkdown("### Home\nLoading…")

	s.memorySearch = widget.NewEntry()
	s.memorySearch.SetPlaceHolder("Search project memory…")
	s.memoryList = widget.NewList(
		func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.memoryItems)
		},
		func() fyne.CanvasObject {
			return container.NewVBox(widget.NewLabel("title"), widget.NewLabel("sub"))
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if id < 0 || id >= len(s.memoryItems) {
				return
			}
			it := s.memoryItems[id]
			box := obj.(*fyne.Container)
			box.Objects[0].(*widget.Label).SetText(first(it.Key, "memory"))
			box.Objects[1].(*widget.Label).SetText(truncate(it.Content, 96))
		},
	)
	s.memoryList.OnSelected = func(id widget.ListItemID) {
		s.mu.Lock()
		var it cloudclient.MemoryItem
		if id >= 0 && id < len(s.memoryItems) {
			it = s.memoryItems[id]
		}
		s.mu.Unlock()
		if it.Key == "" && it.Content == "" {
			return
		}
		s.setPreview("Memory · "+first(it.Key, it.ID), formatMemoryPreview(it))
	}

	s.harvestList = widget.NewList(
		func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.harvestRows)
		},
		func() fyne.CanvasObject {
			return container.NewVBox(widget.NewLabel("t"), widget.NewLabel("s"))
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if id < 0 || id >= len(s.harvestRows) {
				return
			}
			row := s.harvestRows[id]
			box := obj.(*fyne.Container)
			box.Objects[0].(*widget.Label).SetText(row.title)
			box.Objects[1].(*widget.Label).SetText(row.subtitle)
		},
	)
	s.harvestList.OnSelected = func(id widget.ListItemID) {
		s.mu.Lock()
		var row harvestRow
		if id >= 0 && id < len(s.harvestRows) {
			row = s.harvestRows[id]
		}
		s.mu.Unlock()
		switch row.kind {
		case "file":
			s.loadFilePreview(row.filePath, "Agent transcript · "+row.title)
		case "event":
			s.setPreview("Harvest event · "+row.title, row.detail)
		default:
			s.setPreview("Agent harness · "+row.title, row.detail)
		}
	}

	s.workspaceList = widget.NewList(
		func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.workspaceRows)
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("row")
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if id < 0 || id >= len(s.workspaceRows) {
				return
			}
			obj.(*widget.Label).SetText(s.workspaceRows[id].title)
		},
	)
	s.workspaceList.OnSelected = func(id widget.ListItemID) {
		s.mu.Lock()
		var path string
		if id >= 0 && id < len(s.workspaceRows) {
			path = s.workspaceRows[id].path
		}
		s.mu.Unlock()
		if path != "" {
			s.loadFilePreview(path, "Workspace file · "+filepath.Base(path))
		}
	}

	s.buildSettingsPage()
	s.center = container.NewStack(s.homePage())

	sidebar := s.buildSidebar()
	mainSplit := container.NewHSplit(
		container.NewBorder(nil, nil, nil, nil, s.center),
		container.NewBorder(
			s.previewHead, nil, nil, nil,
			container.NewScroll(s.previewBody),
		),
	)
	mainSplit.SetOffset(0.62)

	root := container.NewBorder(
		container.NewVBox(s.statusLine, widget.NewSeparator()),
		nil,
		sidebar,
		nil,
		mainSplit,
	)
	win.SetContent(container.NewPadded(root))
	win.Resize(fyne.NewSize(1120, 720))
	win.SetCloseIntercept(func() {
		win.Hide()
	})
	return s
}

func (s *Shell) buildSidebar() fyne.CanvasObject {
	brand := widget.NewLabelWithStyle("Nexus", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sub := widget.NewLabelWithStyle("Desktop", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
	sub.Importance = widget.LowImportance

	nav := func(label string, sec section) fyne.CanvasObject {
		btn := widget.NewButton(label, func() {
			s.switchSection(sec)
		})
		btn.Alignment = widget.ButtonAlignLeading
		return btn
	}

	return container.NewBorder(
		container.NewVBox(brand, sub, widget.NewSeparator()),
		nil, nil, nil,
		container.NewVBox(
			nav("Home", secHome),
			nav("Memory", secMemory),
			nav("Harvest", secHarvest),
			nav("Workspace files", secWorkspace),
			widget.NewSeparator(),
			nav("Settings", secSettings),
		),
	)
}

func (s *Shell) switchSection(sec section) {
	s.section = sec
	var page fyne.CanvasObject
	switch sec {
	case secMemory:
		page = s.memoryPage()
	case secHarvest:
		page = s.harvestPage()
	case secWorkspace:
		page = s.workspacePage()
	case secSettings:
		page = s.settingsBox
	default:
		page = s.homePage()
	}
	s.center.Objects = []fyne.CanvasObject{page}
	s.center.Refresh()
}

func (s *Shell) homePage() fyne.CanvasObject {
	scan := widget.NewButton("Scan workspace now", func() {
		if err := s.client.TriggerHarvest(); err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.Refresh()
	})
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() {
		if s.hooks.EnsureDaemon != nil {
			_ = s.hooks.EnsureDaemon()
		}
		s.Refresh()
	})
	return container.NewBorder(
		container.NewHBox(refresh, scan),
		nil, nil, nil,
		container.NewScroll(s.homeStats),
	)
}

func (s *Shell) memoryPage() fyne.CanvasObject {
	searchBtn := widget.NewButton("Search", func() {
		go s.runMemorySearch()
	})
	s.memorySearch.OnSubmitted = func(string) {
		go s.runMemorySearch()
	}
	hint := widget.NewLabel("Memory entries are durable facts in Nexus cloud — not files on disk.")
	hint.Wrapping = fyne.TextWrapWord
	hint.Importance = widget.LowImportance
	return container.NewBorder(
		container.NewVBox(hint, container.NewBorder(nil, nil, nil, searchBtn, s.memorySearch)),
		nil, nil, nil,
		s.memoryList,
	)
}

func (s *Shell) harvestPage() fyne.CanvasObject {
	hint := widget.NewLabel("Harvest watches agent transcript files (Cursor, Claude Code, …) under your workspace and syncs turns to the portal.")
	hint.Wrapping = fyne.TextWrapWord
	hint.Importance = widget.LowImportance
	return container.NewBorder(hint, nil, nil, nil, s.harvestList)
}

func (s *Shell) workspacePage() fyne.CanvasObject {
	hint := widget.NewLabel("Workspace files are paths on your machine (repo sources, configs). Agent transcripts also appear under Harvest.")
	hint.Wrapping = fyne.TextWrapWord
	hint.Importance = widget.LowImportance
	pick := widget.NewButton("Choose workspace folder…", func() {
		if s.hooks.OnPickFolder != nil {
			s.hooks.OnPickFolder()
		}
	})
	return container.NewBorder(
		container.NewVBox(hint, pick),
		nil, nil, nil,
		s.workspaceList,
	)
}

func (s *Shell) buildSettingsPage() {
	signIn := widget.NewButton("Sign in", func() {
		if s.hooks.OnSignIn != nil {
			s.hooks.OnSignIn()
		}
	})
	signIn.Importance = widget.HighImportance
	signOut := widget.NewButton("Sign out", func() {
		if s.hooks.OnSignOut != nil {
			s.hooks.OnSignOut()
		}
		s.Refresh()
	})
	folder := widget.NewButton("Choose workspace…", func() {
		if s.hooks.OnPickFolder != nil {
			s.hooks.OnPickFolder()
		}
	})
	updateBtn := widget.NewButton("Check for updates", func() {
		if s.hooks.OnCheckUpdate != nil {
			s.hooks.OnCheckUpdate()
		}
	})
	webPortal := widget.NewButton("Team, org & billing (web)", func() {
		if s.hooks.OnOpenWebPortal != nil {
			s.hooks.OnOpenWebPortal()
		}
	})
	webPortal.Importance = widget.LowImportance
	quit := widget.NewButton("Quit Nexus", func() {
		if s.hooks.OnQuit != nil {
			s.hooks.OnQuit()
		}
	})

	note := widget.NewLabel("Full parity for Team, Org, Overlays, and Admin stays on the web portal until the embedded shell ships (see docs/native-app-direction.md).")
	note.Wrapping = fyne.TextWrapWord
	note.Importance = widget.LowImportance

	s.settingsBox = container.NewVBox(
		widget.NewLabelWithStyle("Settings", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		note,
		widget.NewSeparator(),
		container.NewHBox(signIn, signOut),
		folder,
		updateBtn,
		widget.NewSeparator(),
		webPortal,
		widget.NewSeparator(),
		quit,
		widget.NewLabel(versionText(s.hooks)),
	)
}

func (s *Shell) runMemorySearch() {
	q := strings.TrimSpace(s.memorySearch.Text)
	if q == "" {
		return
	}
	s.mu.Lock()
	pid := ""
	if s.cachedHarvest != nil {
		pid = strings.TrimSpace(s.cachedHarvest.ProjectID)
	}
	if pid == "" && s.cachedStatus != nil {
		pid = strings.TrimSpace(s.cachedStatus.WorkspaceID)
	}
	s.mu.Unlock()

	items, usedPID, err := s.cloud.MemorySearch(q, pid, 25)
	fyne.Do(func() {
		if err != nil {
			dialog.ShowError(err, s.win)
			return
		}
		s.mu.Lock()
		s.memoryItems = items
		s.mu.Unlock()
		s.memoryList.Refresh()
		if len(items) == 0 {
			s.setPreview("Memory search", "No results for \""+q+"\"."+(pidHint(usedPID)))
			return
		}
		s.setPreview("Memory search", fmt.Sprintf("%d results for \"%s\"%s\n\nSelect a row to preview.", len(items), q, pidHint(usedPID)))
	})
}

func pidHint(projectID string) string {
	if projectID == "" {
		return "\n\nTip: link a project in the portal Connect page so search scopes correctly."
	}
	return "\n\nProject: " + projectID
}

func (s *Shell) loadFilePreview(path, title string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	go func() {
		fr, err := s.client.ReadFile(path)
		fyne.Do(func() {
			if err != nil {
				// Transcript paths are often absolute outside repo — show path + error.
				s.setPreview(title, "Path: "+path+"\n\nCould not read via daemon (workspace-relative paths only):\n"+err.Error())
				return
			}
			body := fr.Content
			if len(body) > 120_000 {
				body = body[:120_000] + "\n\n… truncated …"
			}
			s.setPreview(title, "Path: "+fr.Path+"\nSize: "+fmt.Sprintf("%d", fr.Size)+" bytes\n\n"+body)
		})
	}()
}

func (s *Shell) setPreview(title, body string) {
	s.previewHead.SetText(title)
	s.previewBody.SetText(body)
}

// Refresh pulls daemon status/harvest into widgets (must run on UI thread via fyne.Do).
func (s *Shell) Refresh() {
	fyne.Do(func() {
		s.cloud = cloudclient.New("", "")
		st, err := s.client.GetStatus()
		if err != nil {
			s.statusLine.SetText("Daemon offline — local harvest and file preview need nexus-daemon on :7272")
			s.mu.Lock()
			s.cachedStatus = nil
			s.cachedHarvest = nil
			s.cachedWorkspace = nil
			s.harvestRows = nil
			s.workspaceRows = nil
			s.mu.Unlock()
			s.harvestList.Refresh()
			s.workspaceList.Refresh()
			s.homeStats.ParseMarkdown("### Home\n**Daemon offline.** Choose a workspace in Settings and ensure you are signed in.")
			return
		}
		s.mu.Lock()
		s.cachedStatus = st
		s.mu.Unlock()

		switch {
		case strings.TrimSpace(st.Root) == "":
			s.statusLine.SetText("No workspace folder — choose one in Settings or Workspace files")
		case st.Connected:
			s.statusLine.SetText("Connected · " + first(st.Username, st.UserID))
		case st.HasToken:
			s.statusLine.SetText("Signed in — linking workspace…")
		default:
			s.statusLine.SetText("Sign in to sync memory and harvest")
		}

		h, err := s.client.GetHarvest()
		if err == nil {
			s.mu.Lock()
			s.cachedHarvest = h
			s.mu.Unlock()
			s.rebuildHarvestRows(h)
		}

		ws, err := s.client.GetWorkspace()
		if err == nil {
			s.mu.Lock()
			s.cachedWorkspace = ws
			s.mu.Unlock()
		}
		s.rebuildWorkspaceRows()

		s.harvestList.Refresh()
		s.workspaceList.Refresh()
		s.homeStats.ParseMarkdown(s.buildHomeMarkdown(st, h))
	})
}

func (s *Shell) rebuildHarvestRows(h *localclient.Harvest) {
	if h == nil {
		return
	}
	var rows []harvestRow
	for _, a := range h.Agents {
		name := first(a.Name, a.Agent, "agent")
		files := a.FilesSeen
		if files == 0 {
			files = a.FileCount
		}
		rows = append(rows, harvestRow{
			kind:     "agent",
			title:    name,
			subtitle: fmt.Sprintf("%s · %d transcript files seen", first(a.Format, a.Kind, "—"), files),
			detail:   fmt.Sprintf("Agent harness **%s** (%s).\n\nThese are **agent transcript files** on disk, not Memory entries.", name, first(a.Format, a.Kind, "")),
		})
	}
	for _, f := range h.Files {
		label := first(f.Name, filepath.Base(f.Path), "transcript")
		rows = append(rows, harvestRow{
			kind:     "file",
			title:    label,
			subtitle: first(f.Agent, "agent") + " · " + first(f.Format, "format"),
			filePath: f.Path,
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
	s.mu.Lock()
	s.harvestRows = rows
	s.mu.Unlock()
}

func (s *Shell) rebuildWorkspaceRows() {
	s.mu.Lock()
	st := s.cachedStatus
	h := s.cachedHarvest
	ws := s.cachedWorkspace
	s.mu.Unlock()

	var rows []workspaceRow
	if ws != nil && ws.Path != "" {
		rows = append(rows, workspaceRow{
			title: "📁 " + first(ws.Project, filepath.Base(ws.Path)) + " (root)",
			path:  ".",
		})
		if ws.Branch != "" {
			rows = append(rows, workspaceRow{
				title: fmt.Sprintf("Git branch %s @ %s", ws.Branch, truncate(ws.Commit, 8)),
				path:  "",
			})
		}
	}
	if h != nil {
		for _, f := range h.Files {
			rows = append(rows, workspaceRow{
				title: "📝 " + first(f.Name, filepath.Base(f.Path)) + " · " + f.Agent,
				path:  f.Path,
			})
		}
	}
	if st != nil && st.Root != "" {
		rows = append(rows, workspaceRow{
			title: "Path: " + st.Root,
			path:  "",
		})
	}
	s.mu.Lock()
	s.workspaceRows = rows
	s.mu.Unlock()
}

func (s *Shell) buildHomeMarkdown(st *localclient.Status, h *localclient.Harvest) string {
	var b strings.Builder
	b.WriteString("### Home\n")
	if st != nil {
		b.WriteString(fmt.Sprintf("- **Account:** %s\n", orDash(first(st.Username, st.UserID))))
		b.WriteString(fmt.Sprintf("- **Server:** %s\n", orDash(st.ServerURL)))
		b.WriteString(fmt.Sprintf("- **Workspace folder:** %s\n", orDash(st.Root)))
	}
	if h != nil {
		b.WriteString(fmt.Sprintf("\n**Harvest** — last scan %s · files/turns %d/%d · active sessions %d · saved/errors %d/%d\n",
			orDash(h.LastScanAt), h.LastScanFiles, h.LastScanTurns, h.ActiveSessions, h.ProposalsSaved, h.ProposalErrors))
		if msg := strings.TrimSpace(h.Message); msg != "" {
			b.WriteString("\n" + msg + "\n")
		}
	}
	b.WriteString("\nUse **Memory** for cloud facts, **Harvest** for agent transcripts, **Workspace files** for repo paths.\n")
	return b.String()
}

func formatMemoryPreview(it cloudclient.MemoryItem) string {
	var b strings.Builder
	if it.Key != "" {
		b.WriteString("Key: " + it.Key + "\n")
	}
	if it.Level != "" || it.Scope != "" {
		b.WriteString(fmt.Sprintf("Level: %s · Scope: %s\n", it.Level, it.Scope))
	}
	if it.Status != "" {
		b.WriteString("Status: " + it.Status + "\n")
	}
	b.WriteString("\n")
	b.WriteString(it.Content)
	return b.String()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// AttachRefresh starts a background poller until Stop.
func (s *Shell) AttachRefresh(every time.Duration) {
	if every <= 0 {
		every = 4 * time.Second
	}
	go func() {
		s.Refresh()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-t.C:
				s.Refresh()
			}
		}
	}()
}

func (s *Shell) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

func (s *Shell) Show() {
	fyne.Do(func() {
		s.win.Show()
		s.win.RequestFocus()
		s.Refresh()
	})
}

func versionText(h Hooks) string {
	if h.UpdateLabel != nil {
		if s := strings.TrimSpace(h.UpdateLabel()); s != "" {
			return s
		}
	}
	v := strings.TrimPrefix(buildinfo.Version, "v")
	if v == "" {
		v = "dev"
	}
	return "Version: " + v
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
