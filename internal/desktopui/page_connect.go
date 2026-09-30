package desktopui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) connectPage() fyne.CanvasObject {
	s.mu.Lock()
	st := s.cachedStatus
	h := s.cachedHarvest
	recent := append([]string(nil), s.recentWorkspaces...)
	signedIn := s.signedIn
	s.mu.Unlock()

	items := buildConnectChecklist(st, h, signedIn)
	rows := make([]fyne.CanvasObject, 0, len(items)+8)
	rows = append(rows, sectionHeading("Connect"), mutedLabel("Get from signed-out to harvesting without leaving the app."))

	for _, it := range items {
		it := it
		title := checklistMark(it.done) + "  " + it.title
		block := container.NewVBox(
			widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			mutedLabel(it.detail),
		)
		var actions []fyne.CanvasObject
		if it.signIn {
			btn := widget.NewButton(it.action, func() {
				if s.hooks.OnSignIn != nil {
					s.hooks.OnSignIn()
				}
			})
			btn.Importance = widget.HighImportance
			actions = append(actions, btn)
		}
		if it.pickWS {
			btn := widget.NewButton(it.action, func() {
				s.pickWorkspaceFolder()
			})
			actions = append(actions, btn)
		}
		if it.goTo == secHarvest && it.done {
			actions = append(actions, leadingButton("Open Harvest", func() {
				s.switchSection(secHarvest)
			}))
		}
		if len(actions) > 0 {
			block.Add(container.NewHBox(actions...))
		}
		rows = append(rows, block, widget.NewSeparator())
	}

	rows = append(rows, sectionHeading("Recent workspaces"))
	if len(recent) == 0 {
		rows = append(rows, mutedLabel("No recent folders yet. Choose a workspace above."))
	} else {
		for _, path := range recent {
			path := path
			rows = append(rows, leadingButton(recentDisplayName(path), func() {
				s.selectWorkspacePath(path)
			}))
		}
	}

	return container.NewBorder(nil, nil, nil, nil, container.NewScroll(container.NewVBox(rows...)))
}
