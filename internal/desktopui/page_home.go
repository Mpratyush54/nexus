package desktopui

import (
	"fmt"
	"strings"

	"central-memory/internal/localclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) homePage() fyne.CanvasObject {
	scan := primaryButton("Scan now", func() {
		if err := s.client.TriggerHarvest(); err != nil {
			s.setPreview("Scan", "Could not start harvest:\n"+err.Error()+"\n\nCheck setup status on this page (daemon + workspace).")
			return
		}
		s.Refresh()
	})
	refresh := outlineButton("Refresh", func() {
		if s.hooks.EnsureDaemon != nil {
			_ = s.hooks.EnsureDaemon()
		}
		s.Refresh()
	})

	header := container.NewBorder(
		nil, nil, nil,
		toolbar(refresh, scan),
		pageHeader("Home", "Setup status, harvest pulse, and what to do next."),
	)

	if s.homeBody == nil {
		s.homeBody = container.NewVBox()
	}
	s.mu.Lock()
	st := s.cachedStatus
	h := s.cachedHarvest
	signedIn := s.signedIn
	recent := append([]string(nil), s.recentWorkspaces...)
	s.mu.Unlock()
	s.rebuildHomeCards(st, h, signedIn, recent)

	return container.NewBorder(
		container.NewVBox(header, widget.NewSeparator()),
		nil, nil, nil,
		container.NewScroll(s.homeBody),
	)
}

func (s *Shell) rebuildHomeCards(st *localclient.Status, h *localclient.Harvest, signedIn bool, recent ...[]string) {
	if s.homeBody == nil {
		return
	}
	var recentList []string
	if len(recent) > 0 {
		recentList = recent[0]
	} else {
		s.mu.Lock()
		recentList = append([]string(nil), s.recentWorkspaces...)
		s.mu.Unlock()
	}

	cards := make([]fyne.CanvasObject, 0, 12)
	cards = append(cards, sectionHeading("Setup"))

	for _, it := range buildConnectChecklist(st, h, signedIn) {
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
		cards = append(cards, card(title, it.detail, actions...))
	}

	cards = append(cards, widget.NewSeparator(), sectionHeading("Recent workspaces"))
	if len(recentList) == 0 {
		cards = append(cards, mutedLabel("No recent folders yet. Choose a workspace above."))
	} else {
		for _, path := range recentList {
			path := path
			btn := outlineButton(recentDisplayName(path), func() {
				s.selectWorkspacePath(path)
			})
			btn.Alignment = widget.ButtonAlignLeading
			cards = append(cards, btn)
		}
	}

	cards = append(cards, widget.NewSeparator(), sectionHeading("Pulse"))

	connBody := "Sign in to link your cloud account."
	var connActions []fyne.CanvasObject
	if !signedIn {
		connActions = append(connActions, primaryButton("Sign in", func() {
			if s.hooks.OnSignIn != nil {
				s.hooks.OnSignIn()
			}
		}))
	} else if st == nil {
		connBody = "Signed in — local daemon is offline. Refresh to start it."
		connActions = append(connActions, outlineButton("Refresh", func() {
			if s.hooks.EnsureDaemon != nil {
				_ = s.hooks.EnsureDaemon()
			}
			s.Refresh()
		}))
	} else if st.Connected {
		connBody = fmt.Sprintf("%s · %s", first(st.Username, st.UserID, "account"), orDash(st.ServerURL))
	} else {
		connBody = "Signed in — linking workspace to the portal…"
	}
	cards = append(cards, card("Account", connBody, connActions...))

	harvestBody := "Daemon offline — harvest pulse unavailable."
	var harvestActions []fyne.CanvasObject
	if h != nil {
		harvestBody = fmt.Sprintf("Last scan %s · files/turns %d/%d\nActive sessions %d · saved/errors %d/%d",
			orDash(h.LastScanAt), h.LastScanFiles, h.LastScanTurns,
			h.ActiveSessions, h.ProposalsSaved, h.ProposalErrors)
		if msg := strings.TrimSpace(h.Message); msg != "" {
			harvestBody += "\n" + msg
		}
		if pid := strings.TrimSpace(h.ProjectID); pid != "" {
			harvestBody += "\nProject: " + pid
		}
		harvestActions = append(harvestActions, outlineButton("Open Harvest", func() { s.switchSection(secHarvest) }))
	}
	cards = append(cards, card("Harvest pulse", harvestBody, harvestActions...))

	nextTitle := "Next step"
	nextBody := "Sign in to get started."
	var nextActions []fyne.CanvasObject
	switch {
	case !signedIn:
		nextActions = append(nextActions, primaryButton("Sign in", func() {
			if s.hooks.OnSignIn != nil {
				s.hooks.OnSignIn()
			}
		}))
	case st == nil || strings.TrimSpace(st.Root) == "":
		nextBody = "Choose a workspace folder so the daemon can watch agent transcripts."
		nextActions = append(nextActions, primaryButton("Choose folder", func() { s.pickWorkspaceFolder() }))
	case h == nil || (h.LastScanFiles == 0 && len(h.Agents) == 0):
		nextBody = "Workspace is set. Run a harvest scan to pick up agent transcripts."
		nextActions = append(nextActions, primaryButton("Scan now", func() {
			_ = s.client.TriggerHarvest()
			s.Refresh()
		}))
	default:
		nextBody = "Memory = cloud facts · Harvest = transcripts · Workspace = repo files."
		nextActions = append(nextActions,
			outlineButton("Memory", func() { s.switchSection(secMemory) }),
			outlineButton("Harvest", func() { s.switchSection(secHarvest) }),
		)
	}
	cards = append(cards, card(nextTitle, nextBody, nextActions...))
	s.homeBody.Objects = cards
	s.homeBody.Refresh()
}
