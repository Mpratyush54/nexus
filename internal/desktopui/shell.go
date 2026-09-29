// Package desktopui is the native Fyne shell for Nexus Desktop (Win/Mac/Linux).
// It replaces browser-to-127.0.0.1 navigation with in-process widgets that call
// the local daemon via internal/localclient and surface updater status.
package desktopui

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"central-memory/internal/buildinfo"
	"central-memory/internal/localclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Hooks lets cmd/nexus-desktop wire sign-in, workspace pick, and updates
// without importing UI details into the updater.
type Hooks struct {
	OnSignIn      func()
	OnSignOut     func()
	OnOpenPortal  func()
	OnPickFolder  func()
	OnCheckUpdate func()
	OnQuit        func()
	UpdateLabel   func() string
	EnsureDaemon  func() error
}

// Shell owns the main window content and a refresh loop.
type Shell struct {
	win    fyne.Window
	client *localclient.Client
	hooks  Hooks

	statusLine    *widget.Label
	messageLine   *widget.Label
	detailServer  *widget.Label
	detailUser    *widget.Label
	detailWS      *widget.Label
	detailRoot    *widget.Label
	detailMachine *widget.Label
	harvestStats  *widget.Label
	harnessList   *widget.List
	agents        []localclient.HarvestAgent
	versionLine   *widget.Label
	daemonHint    *widget.Label

	mu     sync.Mutex
	stopCh chan struct{}
}

// NewShell builds the native window content. Call AttachRefresh after Show.
func NewShell(win fyne.Window, client *localclient.Client, hooks Hooks) *Shell {
	if client == nil {
		client = localclient.New("")
	}
	s := &Shell{
		win:    win,
		client: client,
		hooks:  hooks,
		stopCh: make(chan struct{}),
	}
	s.statusLine = widget.NewLabel("Checking connection…")
	s.statusLine.TextStyle = fyne.TextStyle{Bold: true}
	s.messageLine = widget.NewLabel("")
	s.messageLine.Wrapping = fyne.TextWrapWord
	s.detailServer = widget.NewLabel("—")
	s.detailUser = widget.NewLabel("—")
	s.detailWS = widget.NewLabel("—")
	s.detailRoot = widget.NewLabel("—")
	s.detailRoot.Wrapping = fyne.TextWrapBreak
	s.detailMachine = widget.NewLabel("—")
	s.harvestStats = widget.NewLabel("Harvest: waiting for daemon…")
	s.versionLine = widget.NewLabel(versionText(hooks))
	s.daemonHint = widget.NewLabel("")
	s.daemonHint.Wrapping = fyne.TextWrapWord

	s.harnessList = widget.NewList(
		func() int {
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.agents)
		},
		func() fyne.CanvasObject {
			return widget.NewLabel("harness")
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if id < 0 || id >= len(s.agents) {
				return
			}
			a := s.agents[id]
			name := first(a.Name, a.Agent, "agent")
			meta := first(a.Format, a.Kind, "—")
			files := a.FilesSeen
			if files == 0 {
				files = a.FileCount
			}
			obj.(*widget.Label).SetText(fmt.Sprintf("%s  ·  %s  ·  files %d", name, meta, files))
		},
	)

	signIn := widget.NewButton("Sign in", func() {
		if hooks.OnSignIn != nil {
			hooks.OnSignIn()
		}
	})
	signIn.Importance = widget.HighImportance
	signOut := widget.NewButton("Sign out", func() {
		if hooks.OnSignOut != nil {
			hooks.OnSignOut()
		}
		s.Refresh()
	})
	portal := widget.NewButton("Open portal", func() {
		if hooks.OnOpenPortal != nil {
			hooks.OnOpenPortal()
		}
	})
	folder := widget.NewButton("Choose workspace…", func() {
		if hooks.OnPickFolder != nil {
			hooks.OnPickFolder()
		}
	})
	scan := widget.NewButton("Scan now", func() {
		if err := s.client.TriggerHarvest(); err != nil {
			dialog.ShowError(err, win)
			return
		}
		s.Refresh()
	})
	updateBtn := widget.NewButton("Check for updates", func() {
		if hooks.OnCheckUpdate != nil {
			hooks.OnCheckUpdate()
		}
		s.versionLine.SetText(versionText(hooks))
	})
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() {
		if hooks.EnsureDaemon != nil {
			_ = hooks.EnsureDaemon()
		}
		s.Refresh()
	})

	connForm := container.NewVBox(
		s.statusLine,
		s.messageLine,
		widget.NewSeparator(),
		labeled("Server", s.detailServer),
		labeled("Account", s.detailUser),
		labeled("Workspace", s.detailWS),
		labeled("Folder", s.detailRoot),
		labeled("Machine", s.detailMachine),
		s.daemonHint,
		container.NewHBox(signIn, signOut, portal),
		container.NewHBox(folder, scan, refresh),
	)

	harvestBox := container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("Harvest", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			s.harvestStats,
		),
		nil, nil, nil,
		s.harnessList,
	)

	footer := container.NewVBox(
		widget.NewSeparator(),
		s.versionLine,
		container.NewHBox(updateBtn),
	)

	content := container.NewBorder(
		nil,
		footer,
		nil, nil,
		container.NewHSplit(connForm, harvestBox),
	)
	win.SetContent(container.NewPadded(content))
	win.Resize(fyne.NewSize(880, 560))
	win.SetCloseIntercept(func() {
		// Close hides to tray; Quit is explicit.
		win.Hide()
	})
	return s
}

func labeled(title string, value *widget.Label) fyne.CanvasObject {
	t := widget.NewLabel(title)
	t.Importance = widget.LowImportance
	return container.NewBorder(nil, nil, t, nil, value)
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

// Refresh pulls daemon status/harvest into widgets (must run on UI thread via fyne.Do).
func (s *Shell) Refresh() {
	fyne.Do(func() {
		s.versionLine.SetText(versionText(s.hooks))
		st, err := s.client.GetStatus()
		if err != nil {
			s.statusLine.SetText("Daemon offline")
			s.messageLine.SetText("Start or reconnect the workspace daemon from the tray / Refresh.")
			s.daemonHint.SetText(err.Error())
			s.harvestStats.SetText("Harvest: unavailable")
			s.mu.Lock()
			s.agents = nil
			s.mu.Unlock()
			s.harnessList.Refresh()
			return
		}
		s.daemonHint.SetText("")
		switch {
		case strings.TrimSpace(st.Root) == "":
			s.statusLine.SetText("No workspace selected")
		case st.Connected:
			s.statusLine.SetText("Connected")
		case st.HasToken:
			s.statusLine.SetText("Signed in — linking…")
		default:
			s.statusLine.SetText("Not connected")
		}
		s.messageLine.SetText(st.Message)
		s.detailServer.SetText(orDash(st.ServerURL))
		s.detailUser.SetText(orDash(first(st.Username, st.UserID)))
		s.detailWS.SetText(orDash(st.WorkspaceID))
		if st.Root != "" {
			s.detailRoot.SetText(filepath.Base(st.Root) + "  —  " + st.Root)
		} else {
			s.detailRoot.SetText("—")
		}
		s.detailMachine.SetText(orDash(st.MachineID))

		h, err := s.client.GetHarvest()
		if err != nil {
			s.harvestStats.SetText("Harvest: " + err.Error())
			return
		}
		s.harvestStats.SetText(fmt.Sprintf(
			"Last scan %s  ·  files/turns %d/%d  ·  active %d  ·  saved/errors %d/%d",
			orDash(h.LastScanAt), h.LastScanFiles, h.LastScanTurns, h.ActiveSessions, h.ProposalsSaved, h.ProposalErrors,
		))
		s.mu.Lock()
		s.agents = append([]localclient.HarvestAgent(nil), h.Agents...)
		s.mu.Unlock()
		s.harnessList.Refresh()
	})
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

// Stop ends the refresh loop.
func (s *Shell) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

// Show brings the window forward.
func (s *Shell) Show() {
	fyne.Do(func() {
		s.win.Show()
		s.win.RequestFocus()
		s.Refresh()
	})
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
