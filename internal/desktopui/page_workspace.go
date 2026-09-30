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
	pick.Importance = widget.HighImportance

	s.mu.Lock()
	recent := append([]string(nil), s.recentWorkspaces...)
	s.mu.Unlock()

	recentBox := container.NewVBox(sectionHeading("Recent"))
	if len(recent) == 0 {
		recentBox.Add(mutedLabel("No recent folders yet."))
	} else {
		for _, path := range recent {
			path := path
			recentBox.Add(leadingButton(recentDisplayName(path), func() {
				s.selectWorkspacePath(path)
			}))
		}
	}

	return container.NewBorder(
		container.NewVBox(
			sectionHeading("Workspace"),
			hint,
			pick,
			recentBox,
			widget.NewSeparator(),
			mutedLabel("Current workspace"),
		),
		nil, nil, nil,
		s.workspaceList,
	)
}
