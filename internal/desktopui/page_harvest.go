package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) harvestPage() fyne.CanvasObject {
	s.mu.Lock()
	n := len(s.harvestRows)
	s.mu.Unlock()

	header := pageHeader("Harvest", "Agent transcript files under your workspace, synced to the portal. Repo sources live under Workspace.")
	var body fyne.CanvasObject = s.harvestList
	if n == 0 {
		body = container.NewBorder(
			mutedLabel("No harvest rows yet. Finish Connect, then Scan on Home or from the tray."),
			nil, nil, nil,
			s.harvestList,
		)
	}
	return container.NewBorder(
		container.NewVBox(header, widget.NewSeparator()),
		nil, nil, nil,
		body,
	)
}
