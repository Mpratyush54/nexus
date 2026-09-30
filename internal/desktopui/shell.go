// Package desktopui is the native Fyne Shell v2 for Nexus Desktop (Win/Mac/Linux).
// Layout: Welcome (signed out) or sidebar + content + preview (signed in).
package desktopui

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"central-memory/internal/buildinfo"
	"central-memory/internal/cloudclient"
	"central-memory/internal/config"
	"central-memory/internal/localclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

// Hooks lets cmd/nexus-desktop wire sign-in, workspace, and updates.
type Hooks struct {
	OnSignIn        func()
	OnSignOut       func()
	OnOpenWebPortal func() // explicit external: team/org/billing only
	OnSetWorkspace  func(path string)
	OnCheckUpdate   func()
	OnQuit          func()
	UpdateLabel     func() string
	EnsureDaemon    func() error
}

// Shell owns the main window content and a refresh loop.
type Shell struct {
	win    fyne.Window
	client *localclient.Client
	cloud  *cloudclient.Client
	hooks  Hooks

	mode    shellMode
	section section

	root        *fyne.Container
	statusLine  *widget.Label
	center      *fyne.Container
	previewHead *widget.Label
	previewBody *widget.Entry

	homeStats *widget.RichText

	memorySearch *widget.Entry
	memoryList   *widget.List
	memoryItems  []cloudclient.MemoryItem
	memoryBanner *fyne.Container

	harvestList   *widget.List
	harvestRows   []harvestRow
	workspaceList *widget.List
	workspaceRows []workspaceRow

	mu               sync.Mutex
	stopCh           chan struct{}
	signedIn         bool
	pickingFolder    bool
	previewKind      string // "", memory, harvest, workspace, system
	cachedStatus     *localclient.Status
	cachedHarvest    *localclient.Harvest
	cachedWorkspace  *localclient.Workspace
	recentWorkspaces []string
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
		mode:   modeWelcome,
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
	s.previewBody.SetPlaceHolder("Select a memory entry, harvest transcript, or workspace file to inspect content here.")
	s.previewBody.Wrapping = fyne.TextWrapWord
	s.previewBody.Disable()

	s.homeStats = widget.NewRichTextFromMarkdown("### Home\nLoading…")
	s.memoryBanner = container.NewVBox()

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
		s.setPreviewKind("memory", "Memory · "+first(it.Key, it.ID), formatMemoryPreview(it))
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
			s.setPreviewKind("harvest", "Harvest event · "+row.title, row.detail)
		default:
			s.setPreviewKind("harvest", "Agent harness · "+row.title, row.detail)
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

	s.center = container.NewStack(s.welcomePage())
	s.root = container.NewStack()
	win.SetContent(container.NewPadded(s.root))
	win.Resize(fyne.NewSize(1120, 720))
	// Close → hide (tray keeps process alive). Quit only from tray / Settings.
	win.SetCloseIntercept(func() {
		win.Hide()
	})

	signedIn := strings.TrimSpace(config.ResolveToken()) != ""
	s.signedIn = signedIn
	s.mode = deriveMode(signedIn)
	s.rebuildChrome()
	return s
}

// pickWorkspaceFolder opens the Fyne folder dialog on all OS (no auto-launch).
// Guarded against panics and skips Refresh rebuilds while the modal is open
// (rebuilding chrome under an open folder dialog has crashed the Windows app).
func (s *Shell) pickWorkspaceFolder() {
	defer func() {
		if r := recover(); r != nil {
			s.mu.Lock()
			s.pickingFolder = false
			s.mu.Unlock()
			s.setPreviewKind("workspace", "Workspace", "Folder picker failed (recovered):\n"+fmt.Sprint(r)+"\n\nUse a Recent folder below, or try again.")
		}
	}()
	if s.win == nil {
		s.setPreviewKind("workspace", "Workspace", "Folder picker unavailable: no window parent.")
		return
	}
	s.mu.Lock()
	if s.pickingFolder {
		s.mu.Unlock()
		return
	}
	s.pickingFolder = true
	s.mu.Unlock()

	finishPick := func() {
		s.mu.Lock()
		s.pickingFolder = false
		s.mu.Unlock()
	}

	d := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		finishPick()
		if err != nil {
			s.setPreviewKind("workspace", "Workspace", "Folder picker error:\n"+err.Error())
			return
		}
		if uri == nil {
			return
		}
		path := folderURIPath(uri)
		if path == "" {
			s.setPreviewKind("workspace", "Workspace", "Could not resolve the selected folder path.")
			return
		}
		s.selectWorkspacePath(path)
	}, s.win)
	d.Resize(fyne.NewSize(720, 480))
	if file, err := config.LoadFile(); err == nil {
		if root := strings.TrimSpace(file.WorkspaceRoot); root != "" {
			func() {
				defer func() { _ = recover() }()
				if u, err := storage.ListerForURI(storage.NewFileURI(root)); err == nil && u != nil {
					d.SetLocation(u)
				}
			}()
		}
	}
	d.Show()
}

func (s *Shell) selectWorkspacePath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if s.hooks.OnSetWorkspace != nil {
		s.hooks.OnSetWorkspace(path)
	}
}

// folderURIPath normalizes a Fyne folder URI to an OS filesystem path.
func folderURIPath(uri fyne.ListableURI) string {
	if uri == nil {
		return ""
	}
	path := uri.Path()
	// Windows file URIs often look like /C:/Users/...
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.Clean(path)
}

// Refresh pulls daemon status/harvest into widgets (must run on UI thread via fyne.Do).
func (s *Shell) Refresh() {
	fyne.Do(func() {
		s.mu.Lock()
		picking := s.pickingFolder
		s.mu.Unlock()
		if picking {
			// Do not rebuild chrome/lists under an open folder dialog — that has
			// panicked/crashed the Windows Fyne shell.
			return
		}

		s.cloud = cloudclient.New("", "")
		signedIn := strings.TrimSpace(config.ResolveToken()) != ""
		prevMode := s.mode
		s.mu.Lock()
		s.signedIn = signedIn
		s.mu.Unlock()
		s.applyMode(signedIn)
		if s.mode != prevMode {
			// chrome already rebuilt
		}

		recent := loadRecentWorkspaces(s.client)
		s.mu.Lock()
		s.recentWorkspaces = recent
		s.mu.Unlock()

		st, err := s.client.GetStatus()
		if err != nil {
			s.statusLine.SetText(buildStatusLine(nil, signedIn))
			s.mu.Lock()
			s.cachedStatus = nil
			s.cachedHarvest = nil
			s.cachedWorkspace = nil
			s.harvestRows = nil
			s.workspaceRows = nil
			s.mu.Unlock()
			s.harvestList.Refresh()
			s.workspaceList.Refresh()
			s.homeStats.ParseMarkdown(buildHomeMarkdown(nil, nil, signedIn))
			if s.mode == modeApp && (s.section == secConnect || s.section == secHome || s.section == secWorkspace) {
				s.renderCenter()
			}
			return
		}
		s.mu.Lock()
		s.cachedStatus = st
		s.mu.Unlock()
		s.statusLine.SetText(buildStatusLine(st, signedIn))

		h, err := s.client.GetHarvest()
		if err == nil {
			s.mu.Lock()
			s.cachedHarvest = h
			s.harvestRows = buildHarvestRows(h)
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.cachedHarvest = nil
			s.harvestRows = nil
			s.mu.Unlock()
		}

		ws, err := s.client.GetWorkspace()
		if err == nil {
			s.mu.Lock()
			s.cachedWorkspace = ws
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.cachedWorkspace = nil
			s.mu.Unlock()
		}

		s.mu.Lock()
		s.workspaceRows = buildWorkspaceRows(s.cachedStatus, s.cachedWorkspace)
		s.mu.Unlock()

		s.harvestList.Refresh()
		s.workspaceList.Refresh()
		s.homeStats.ParseMarkdown(buildHomeMarkdown(st, h, signedIn))
		if s.mode == modeApp && (s.section == secConnect || s.section == secHome || s.section == secWorkspace || s.section == secHarvest) {
			s.renderCenter()
		}
	})
}

func loadRecentWorkspaces(client *localclient.Client) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(list []string) {
		for _, p := range list {
			p = config.NormalizeWorkspacePath(strings.TrimSpace(p))
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
			if len(out) >= 5 {
				return
			}
		}
	}
	if file, err := config.LoadFile(); err == nil {
		add(file.RecentWorkspaces)
		if r := strings.TrimSpace(file.WorkspaceRoot); r != "" {
			add([]string{r})
		}
	}
	if client != nil {
		if remote, err := client.RecentWorkspaces(); err == nil {
			add(remote)
		}
	}
	return out
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
