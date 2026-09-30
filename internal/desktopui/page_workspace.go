package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) workspacePage() fyne.CanvasObject {
	pick := primaryButton("Choose folder", func() {
		s.pickWorkspaceFolder()
	})
	pick.Icon = theme.FolderIcon()

	s.mu.Lock()
	recent := append([]string(nil), s.recentWorkspaces...)
	s.mu.Unlock()

	recentBox := container.NewVBox(sectionHeading("Recent"))
	if len(recent) == 0 {
		recentBox.Add(mutedLabel("No recent folders yet."))
	} else {
		for _, path := range recent {
			path := path
			btn := outlineButton(recentDisplayName(path), func() {
				s.selectWorkspacePath(path)
			})
			btn.Alignment = widget.ButtonAlignLeading
			recentBox.Add(btn)
		}
	}

	header := container.NewBorder(
		nil, nil, nil,
		toolbar(pick),
		pageHeader("Workspace", "Your chosen repo root. Agent transcripts are under Harvest — not listed here."),
	)

	return container.NewBorder(
		container.NewVBox(
			header,
			widget.NewSeparator(),
			recentBox,
			widget.NewSeparator(),
			mutedLabel("Current workspace"),
		),
		nil, nil, nil,
		s.workspaceList,
	)
}
