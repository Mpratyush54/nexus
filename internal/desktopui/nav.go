package desktopui

import (
	"central-memory/internal/localclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) buildSidebar() fyne.CanvasObject {
	nav := []struct {
		label string
		icon  fyne.Resource
		sec   section
	}{
		{"Home", theme.HomeIcon(), secHome},
		{"Memory", theme.DocumentIcon(), secMemory},
		{"Harvest", theme.ListIcon(), secHarvest},
		{"Sessions", theme.MediaReplayIcon(), secSessions},
		{"Workspace", theme.FolderIcon(), secWorkspace},
	}

	items := make([]fyne.CanvasObject, 0, len(nav)+4)
	items = append(items, brandBlock(), widget.NewSeparator())
	for _, n := range nav {
		n := n
		items = append(items, navRow(n.label, n.icon, s.section == n.sec, func() {
			s.switchSection(n.sec)
		}))
	}
	items = append(items, widget.NewSeparator())
	items = append(items, navRow("Settings", theme.SettingsIcon(), s.section == secSettings, func() {
		s.switchSection(secSettings)
	}))

	body := paneBG(
		container.NewBorder(
			nil, nil, nil, nil,
			container.NewVBox(items...),
		),
		colorSurface,
	)
	return withFixedWidth(sidebarWidth, body)
}

func (s *Shell) switchSection(sec section) {
	if sec == secConnect {
		sec = secHome // Connect lives on Home now
	}
	s.section = sec
	s.clearPreviewForSection(sec)
	s.rebuildChrome()
	if sec == secMemory {
		go s.runMemorySearch(true)
	}
	if sec == secSessions {
		go s.loadSessionSnapshots()
	}
}

func (s *Shell) renderCenter() {
	var page fyne.CanvasObject
	if s.mode == modeWelcome {
		page = s.welcomePage()
	} else {
		switch s.section {
		case secMemory:
			page = s.memoryPage()
		case secHarvest:
			page = s.harvestPage()
		case secSessions:
			page = s.sessionsPage()
		case secWorkspace:
			page = s.workspacePage()
		case secSettings:
			page = s.settingsPage()
		default:
			page = s.homePage()
		}
	}
	s.center.Objects = []fyne.CanvasObject{page}
	s.center.Refresh()
}

func (s *Shell) applyMode(signedIn bool) {
	next := deriveMode(signedIn)
	if next == s.mode {
		return
	}
	s.mode = next
	if next == modeWelcome {
		s.section = secHome
	}
	s.rebuildChrome()
}

func (s *Shell) sectionShowsPreview() bool {
	if s.mode != modeApp {
		return false
	}
	switch s.section {
	case secMemory, secHarvest, secWorkspace, secSessions:
		return true
	default:
		return false
	}
}

func (s *Shell) rebuildChrome() {
	if s.topChrome == nil {
		s.topChrome = s.buildTopChrome()
	} else {
		s.refreshTopChrome(s.cachedStatus, s.signedIn)
	}
	top := s.topChrome
	if s.mode == modeWelcome {
		s.root.Objects = []fyne.CanvasObject{
			container.NewBorder(top, nil, nil, nil, s.center),
		}
	} else {
		content := withMinWidth(contentMinW, paneBG(container.NewPadded(s.center), colorBase))
		var main fyne.CanvasObject = content
		if s.sectionShowsPreview() {
			if s.previewPane == nil {
				s.previewPane = s.buildPreviewPane()
			}
			split := container.NewHSplit(content, s.previewPane)
			split.SetOffset(previewSplit)
			main = split
		}
		s.root.Objects = []fyne.CanvasObject{
			container.NewBorder(
				top,
				nil,
				s.buildSidebar(),
				nil,
				main,
			),
		}
	}
	s.root.Refresh()
	s.renderCenter()
}

func (s *Shell) buildTopChrome() fyne.CanvasObject {
	s.refreshTopChrome(s.cachedStatus, s.signedIn)

	chipBG := canvas.NewRectangle(colorRaised)
	chipBG.CornerRadius = 10
	chipBG.StrokeColor = colorBorder
	chipBG.StrokeWidth = 1
	pill := container.NewStack(chipBG, container.NewPadded(container.NewHBox(
		container.NewCenter(container.New(&dotSize{}, s.statusDot)),
		s.statusText,
	)))

	left := container.NewHBox(pill, s.accountTxt)
	barBG := paneBG(container.NewPadded(left), colorSurface)
	return container.NewVBox(barBG, widget.NewSeparator())
}

func (s *Shell) refreshTopChrome(st *localclient.Status, signedIn bool) {
	connected := st != nil && st.Connected
	pillLabel := "Offline"
	account := ""
	if st != nil && st.Connected {
		pillLabel = "Connected"
		account = first(st.Username, st.UserID)
	} else if signedIn {
		pillLabel = "Signed in"
		if st != nil {
			account = first(st.Username, st.UserID)
		}
	} else if s.mode == modeWelcome {
		pillLabel = "Sign in"
	}

	if s.statusDot != nil {
		if connected {
			s.statusDot.FillColor = colorTeal
		} else {
			s.statusDot.FillColor = emberAccent
		}
		s.statusDot.Refresh()
	}
	if s.statusText != nil {
		s.statusText.Text = pillLabel
		s.statusText.Refresh()
	}
	if s.accountTxt != nil {
		s.accountTxt.Text = account
		s.accountTxt.Refresh()
	}
	if s.statusLine != nil {
		s.statusLine.SetText(buildStatusLine(st, signedIn))
	}
}
