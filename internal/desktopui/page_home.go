package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) homePage() fyne.CanvasObject {
	scan := widget.NewButton("Scan workspace now", func() {
		if err := s.client.TriggerHarvest(); err != nil {
			s.setPreview("Scan", "Could not start harvest:\n"+err.Error()+"\n\nOpen Connect to verify daemon and workspace.")
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
	connect := leadingButton("Open Connect checklist", func() {
		s.switchSection(secConnect)
	})
	return container.NewBorder(
		container.NewVBox(
			sectionHeading("Home"),
			container.NewHBox(refresh, scan, connect),
			widget.NewSeparator(),
		),
		nil, nil, nil,
		container.NewScroll(s.homeStats),
	)
}
