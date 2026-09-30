package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) buildSidebar() fyne.CanvasObject {
	brand := widget.NewLabelWithStyle("Nexus", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sub := mutedLabel("Desktop")

	nav := []struct {
		label string
		sec   section
	}{
		{"Home", secHome},
		{"Connect", secConnect},
		{"Memory", secMemory},
		{"Harvest", secHarvest},
		{"Workspace", secWorkspace},
	}

	items := make([]fyne.CanvasObject, 0, len(nav)+3)
	for _, n := range nav {
		n := n
		btn := widget.NewButton(n.label, func() { s.switchSection(n.sec) })
		btn.Alignment = widget.ButtonAlignLeading
		if s.section == n.sec {
			btn.Importance = widget.MediumImportance
		} else {
			btn.Importance = widget.LowImportance
		}
		items = append(items, btn)
	}
	items = append(items, widget.NewSeparator())
	settings := widget.NewButton("Settings", func() { s.switchSection(secSettings) })
	settings.Alignment = widget.ButtonAlignLeading
	if s.section == secSettings {
		settings.Importance = widget.MediumImportance
	} else {
		settings.Importance = widget.LowImportance
	}
	items = append(items, settings)

	return container.NewBorder(
		container.NewVBox(brand, sub, widget.NewSeparator()),
		nil, nil, nil,
		container.NewVBox(items...),
	)
}

func (s *Shell) switchSection(sec section) {
	s.section = sec
	s.clearPreviewForSection(sec)
	s.rebuildChrome()
}

func (s *Shell) renderCenter() {
	var page fyne.CanvasObject
	if s.mode == modeWelcome {
		page = s.welcomePage()
	} else {
		switch s.section {
		case secConnect:
			page = s.connectPage()
		case secMemory:
			page = s.memoryPage()
		case secHarvest:
			page = s.harvestPage()
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
	} else if s.section == secHome {
		// keep Home
	}
	s.rebuildChrome()
}

func (s *Shell) rebuildChrome() {
	if s.mode == modeWelcome {
		s.root.Objects = []fyne.CanvasObject{
			container.NewBorder(
				container.NewVBox(s.statusLine, widget.NewSeparator()),
				nil, nil, nil,
				s.center,
			),
		}
	} else {
		mainSplit := container.NewHSplit(
			container.NewBorder(nil, nil, nil, nil, s.center),
			container.NewBorder(
				s.previewHead, nil, nil, nil,
				container.NewScroll(s.previewBody),
			),
		)
		mainSplit.SetOffset(0.62)
		s.root.Objects = []fyne.CanvasObject{
			container.NewBorder(
				container.NewVBox(s.statusLine, widget.NewSeparator()),
				nil,
				s.buildSidebar(),
				nil,
				mainSplit,
			),
		}
	}
	s.root.Refresh()
	s.renderCenter()
}
