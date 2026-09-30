package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) workspacePage() fyne.CanvasObject {
	hint := mutedLabel("Workspace is your chosen repo root. Agent transcripts are under Harvest — not listed here.")
	pick := widget.NewButton("Choose workspace folder…", func() {
		s.pickWorkspaceFolder()
	})
	// Medium — HighImportance painted a full-width orange slab that read as placeholder UI.
	pick.Importance = widget.MediumImportance
	pick.Alignment = widget.ButtonAlignLeading

	s.mu.Lock()
	recent := append([]string(nil), s.recentWorkspaces...)
	s.mu.Unlock()

	recentBox := container.NewVBox(sectionHeading("Recent"))
	if len(recent) == 0 {
		recentBox.Add(mutedLabel("No recent folders yet."))
	} else {
		for _, path := range recent {
			path := path
			btn := widget.NewButton(recentDisplayName(path), func() {
				s.selectWorkspacePath(path)
			})
			btn.Alignment = widget.ButtonAlignLeading
			btn.Importance = widget.LowImportance
			recentBox.Add(btn)
		}
	}

	return container.NewBorder(
		container.NewVBox(
			sectionHeading("Workspace"),
			hint,
			container.NewHBox(pick),
			recentBox,
			widget.NewSeparator(),
			mutedLabel("Current workspace"),
		),
		nil, nil, nil,
		s.workspaceList,
	)
}
