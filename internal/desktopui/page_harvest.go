package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) harvestPage() fyne.CanvasObject {
	hint := mutedLabel("Harvest watches agent transcript files (Cursor, Claude Code, …) under your workspace and syncs turns to the portal. Repo sources live under Workspace.")
	empty := mutedLabel("No harvest rows yet. Finish Connect, then Scan on Home or from the tray.")
	s.mu.Lock()
	n := len(s.harvestRows)
	s.mu.Unlock()
	body := fyne.CanvasObject(s.harvestList)
	if n == 0 {
		body = container.NewBorder(empty, nil, nil, nil, s.harvestList)
	}
	return container.NewBorder(
		container.NewVBox(sectionHeading("Harvest"), hint, widget.NewSeparator()),
		nil, nil, nil,
		body,
	)
}
