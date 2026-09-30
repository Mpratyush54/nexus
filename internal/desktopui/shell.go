package desktopui

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"central-memory/internal/buildinfo"
	"central-memory/internal/cloudclient"
	"central-memory/internal/config"
	"central-memory/internal/localclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
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

	root       *fyne.Container
	topChrome  fyne.CanvasObject
	statusLine *widget.Label // legacy sync text; chrome uses pill widgets
	statusDot  *canvas.Circle
	statusText *canvas.Text
	accountTxt *canvas.Text
	center     *fyne.Container
	homeBody   *fyne.Container
	homeUI     *homeWidgets

	previewHead   *widget.Label
	previewMeta   *widget.Label
	previewLinks  *fyne.Container
	previewTurns  *fyne.Container
	previewScroll *container.Scroll
	previewPane   fyne.CanvasObject

	memorySearch   *widget.Entry
	memoryLevel    *widget.Select
	memoryStatus   *widget.Select
	memoryCategory *widget.Select
	memoryTags     *widget.Entry
	memoryList     *widget.List
	memoryItems    []cloudclient.MemoryItem
	memoryBanner   *fyne.Container

	sessionList  *widget.List
	sessionRows  []sessionRow
	sessionBanner *fyne.Container

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
	cachedDashboard  *cloudclient.ProjectDashboard
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
		cloud:  cloudclient.NewDesktop(),
		hooks:  hooks,
		stopCh: make(chan struct{}),
		mode:   modeWelcome,
	}
	win.SetTitle("Nexus")
	if app := fyne.CurrentApp(); app != nil {
		app.Settings().SetTheme(newNexusTheme())
	}

	s.statusLine = widget.NewLabel("Checking connection…")
	s.statusLine.Hide()
	s.statusDot = canvas.NewCircle(colorMuted)
	s.statusText = canvas.NewText("Checking…", colorFg)
	s.statusText.TextSize = 12
	s.accountTxt = canvas.NewText("", colorFgDim)
	s.accountTxt.TextSize = 12
	s.topChrome = s.buildTopChrome()

	s.previewHead = widget.NewLabelWithStyle("Preview", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	s.previewMeta = mutedLabel("")
	s.previewMeta.Hide()
	s.previewTurns = container.NewVBox()
	s.previewPane = s.buildPreviewPane()
	s.renderPreviewPlain(previewEmptyMessage(secHome), false)

	s.homeBody = container.NewVBox()
	s.memoryBanner = container.NewVBox()
	s.sessionBanner = container.NewVBox()

	s.memorySearch = widget.NewEntry()
	s.memorySearch.SetPlaceHolder("Search project memory…")
	s.memoryLevel = widget.NewSelect([]string{"All levels", "project", "personal", "organization", "session"}, nil)
	s.memoryLevel.SetSelected("All levels")
	s.memoryStatus = widget.NewSelect([]string{"All status", "PROPOSED", "CONFIRMED"}, nil)
	s.memoryStatus.SetSelected("All status")
	s.memoryCategory = widget.NewSelect([]string{
		"All categories", "architecture", "infrastructure", "auth", "api", "conventions", "dependencies", "general",
	}, nil)
	s.memoryCategory.SetSelected("All categories")
	s.memoryTags = widget.NewEntry()
	s.memoryTags.SetPlaceHolder("Tags (comma-separated)")
	s.memoryList = widget.NewList(
		func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.memoryItems)
		},
		listRowTemplate,
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if id < 0 || id >= len(s.memoryItems) {
				return
			}
			it := s.memoryItems[id]
			updateListRow(obj, first(it.Key, "memory"), truncate(it.Content, 96))
		},
	)
	s.memoryList.OnSelected = func(id widget.ListItemID) {
		s.mu.Lock()
		var it cloudclient.MemoryItem
		if id >= 0 && id < len(s.memoryItems) {
			it = s.memoryItems[id]
		}
		s.mu.Unlock()
		if it.Key == "" && it.Content == "" && it.ID == "" {
			return
		}
		s.showMemoryPreview(it)
	}

	s.sessionList = widget.NewList(
		func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.sessionRows)
		},
		listRowTemplate,
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if id < 0 || id >= len(s.sessionRows) {
				return
			}
			row := s.sessionRows[id]
			updateListRow(obj, row.title, row.subtitle)
		},
	)
	s.sessionList.OnSelected = func(id widget.ListItemID) {
		s.mu.Lock()
		var row sessionRow
		if id >= 0 && id < len(s.sessionRows) {
			row = s.sessionRows[id]
		}
		s.mu.Unlock()
		if row.sessionID == "" {
			return
		}
		s.showSessionPreview(row)
	}

	s.harvestList = widget.NewList(
		func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.harvestRows)
		},
		listRowTemplate,
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if id < 0 || id >= len(s.harvestRows) {
				return
			}
			row := s.harvestRows[id]
			updateListRow(obj, row.title, row.subtitle)
		},
	)
	s.harvestList.OnSelected = func(id widget.ListItemID) {
		s.mu.Lock()
		var row harvestRow
		if id >= 0 && id < len(s.harvestRows) {
			row = s.harvestRows[id]
		}
		h := s.cachedHarvest
		s.mu.Unlock()
		switch row.kind {
		case "file":
			if s.previewLinks != nil {
				s.previewLinks.Objects = nil
				s.previewLinks.Hide()
				s.previewLinks.Refresh()
			}
			s.loadHarvestTranscript(row.filePath, row.title, row.format)
		case "agent":
			s.showHarnessPreview(row, h)
		case "event":
			s.setPreviewKind("harvest", "Harvest event · "+row.title, row.detail)
		default:
			s.setPreviewKind("harvest", "Harvest · "+row.title, row.detail)
		}
	}

	s.workspaceList = widget.NewList(
		func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.workspaceRows)
		},
		listRowTemplate,
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if id < 0 || id >= len(s.workspaceRows) {
				return
			}
			row := s.workspaceRows[id]
			sub := row.path
			if sub == "" {
				sub = " "
			}
			updateListRow(obj, row.title, sub)
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
	win.SetContent(s.root)
	win.Resize(fyne.NewSize(1280, 780))
	win.SetCloseIntercept(func() {
		win.Hide()
	})

	signedIn := strings.TrimSpace(config.ResolveDesktopToken()) != ""
	s.signedIn = signedIn
	s.mode = deriveMode(signedIn)
	s.rebuildChrome()
	return s
}

// pickWorkspaceFolder opens the Fyne folder dialog on all OS (no auto-launch).
//
// ROOT CAUSE (fixed): FileDialog.Resize before Show panics — MinSize() nil-derefs
// dialog.win which only exists after Show. Always Show first, then Resize.
func (s *Shell) pickWorkspaceFolder() {
	if s.win == nil {
		s.setPreviewKind("workspace", "Workspace", "Folder picker unavailable: no window parent.")
		return
	}
	if s.win.Canvas() == nil {
		s.setPreviewKind("workspace", "Workspace", "Folder picker unavailable: window canvas not ready.")
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
			return // cancel
		}
		path := folderURIPath(uri)
		if path == "" {
			s.setPreviewKind("workspace", "Workspace", "Could not resolve the selected folder path.")
			return
		}
		s.selectWorkspacePath(path)
	}, s.win)

	// SetLocation before Show is safe — it only sets startingLocation.
	if file, err := config.LoadFile(); err == nil {
		if root := strings.TrimSpace(file.WorkspaceRoot); root != "" {
			if u, err := storage.ListerForURI(storage.NewFileURI(root)); err == nil && u != nil {
				d.SetLocation(u)
			}
		}
	}

	// Show creates dialog.win; Resize after that is safe.
	d.Show()
	d.Resize(fyne.NewSize(720, 480))
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
			return
		}

		// Prefer config.json session token over stale NEXUS_TOKEN env (root cause of Memory 401).
		s.cloud = cloudclient.NewDesktop()
		signedIn := strings.TrimSpace(config.ResolveDesktopToken()) != ""
		prevMode := s.mode
		s.mu.Lock()
		s.signedIn = signedIn
		s.mu.Unlock()
		s.applyMode(signedIn)
		_ = prevMode

		recent := loadRecentWorkspaces(s.client)
		s.mu.Lock()
		s.recentWorkspaces = recent
		s.mu.Unlock()

		st, err := s.client.GetStatus()
		if err != nil {
			s.mu.Lock()
			s.cachedStatus = nil
			s.cachedHarvest = nil
			s.cachedWorkspace = nil
			s.cachedDashboard = nil
			s.harvestRows = nil
			s.workspaceRows = nil
			s.mu.Unlock()
			s.harvestList.Refresh()
			s.workspaceList.Refresh()
			s.rebuildHomeCards(nil, nil, signedIn)
			s.refreshTopChrome(nil, signedIn)
			// Do not remount Home — scroll would jump. Only remount list pages if needed.
			if s.mode == modeApp && (s.section == secWorkspace || s.section == secHarvest) {
				s.renderCenter()
			}
			return
		}
		s.mu.Lock()
		s.cachedStatus = st
		s.mu.Unlock()

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
		pid := memoryProjectID(s.cachedStatus, s.cachedHarvest)
		s.mu.Unlock()

		s.harvestList.Refresh()
		s.workspaceList.Refresh()

		// Kick dashboard fetch off-thread; Home updates when it returns.
		if signedIn && pid != "" {
			go s.fetchDashboard(pid)
		} else {
			s.mu.Lock()
			s.cachedDashboard = nil
			s.mu.Unlock()
		}

		s.rebuildHomeCards(st, h, signedIn)
		s.refreshTopChrome(st, signedIn)
		// Home updates in place via rebuildHomeCards — remounting reset scroll.
		if s.mode == modeApp && (s.section == secWorkspace || s.section == secHarvest) {
			s.renderCenter()
		}
	})
}

func (s *Shell) fetchDashboard(projectID string) {
	c := s.cloud
	if c == nil {
		c = cloudclient.NewDesktop()
	}
	dash, err := c.ProjectDashboard(projectID)
	fyne.Do(func() {
		s.mu.Lock()
		if err != nil {
			s.cachedDashboard = nil
		} else {
			s.cachedDashboard = dash
		}
		st := s.cachedStatus
		h := s.cachedHarvest
		signedIn := s.signedIn
		recent := append([]string(nil), s.recentWorkspaces...)
		cached := s.cachedDashboard
		s.mu.Unlock()
		if s.homeUI != nil {
			s.updateHomeDashboard(st, h, signedIn, recent, cached)
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

// folderDialogResizeAfterShow documents the Fyne ordering invariant for tests.
func folderDialogResizeAfterShow() string {
	return "Show before Resize — FileDialog.MinSize nil-derefs dialog.win until Show"
}
