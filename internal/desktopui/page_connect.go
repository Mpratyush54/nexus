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
	rows = append(rows, pageHeader("Connect", "Get from signed-out to harvesting without leaving the app."))
	rows = append(rows, widget.NewSeparator())

	for _, it := range items {
		it := it
		status := "Pending"
		if it.done {
			status = "Done"
		}
		title := it.title + "  ·  " + status
		var actions []fyne.CanvasObject
		if it.signIn {
			actions = append(actions, primaryButton(it.action, func() {
				if s.hooks.OnSignIn != nil {
					s.hooks.OnSignIn()
				}
			}))
		}
		if it.pickWS {
			label := it.action
			if label == "" {
				label = "Choose folder"
			}
			actions = append(actions, secondaryButton(label, func() {
				s.pickWorkspaceFolder()
			}))
		}
		if it.goTo == secHarvest && it.done {
			actions = append(actions, outlineButton("Open Harvest", func() {
				s.switchSection(secHarvest)
			}))
		}
		rows = append(rows, card(title, it.detail, actions...))
	}

	rows = append(rows, widget.NewSeparator(), sectionHeading("Recent workspaces"))
	if len(recent) == 0 {
		rows = append(rows, mutedLabel("No recent folders yet. Choose a workspace above."))
	} else {
		for _, path := range recent {
			path := path
			btn := outlineButton(recentDisplayName(path), func() {
				s.selectWorkspacePath(path)
			})
			btn.Alignment = widget.ButtonAlignLeading
			rows = append(rows, btn)
		}
	}

	return container.NewBorder(nil, nil, nil, nil, container.NewScroll(container.NewVBox(rows...)))
}
