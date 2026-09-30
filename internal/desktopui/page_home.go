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
			s.setPreview("Scan", "Could not start harvest:\n"+err.Error()+"\n\nOpen Connect to verify daemon and workspace.")
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
	connect := outlineButton("Open Connect", func() {
		s.switchSection(secConnect)
	})

	header := container.NewBorder(
		nil, nil, nil,
		toolbar(refresh, scan, connect),
		pageHeader("Home", "Connection, harvest pulse, and what to do next."),
	)

	if s.homeBody == nil {
		s.homeBody = container.NewVBox()
	}
	s.mu.Lock()
	st := s.cachedStatus
	h := s.cachedHarvest
	signedIn := s.signedIn
	s.mu.Unlock()
	s.rebuildHomeCards(st, h, signedIn)

	return container.NewBorder(
		container.NewVBox(header, widget.NewSeparator()),
		nil, nil, nil,
		container.NewScroll(s.homeBody),
	)
}

func (s *Shell) rebuildHomeCards(st *localclient.Status, h *localclient.Harvest, signedIn bool) {
	if s.homeBody == nil {
		return
	}
	cards := make([]fyne.CanvasObject, 0, 4)

	connBody := "Sign in to link your cloud account."
	var connActions []fyne.CanvasObject
	if !signedIn {
		connActions = append(connActions, primaryButton("Sign in", func() {
			if s.hooks.OnSignIn != nil {
				s.hooks.OnSignIn()
			}
		}))
	} else if st == nil {
		connBody = "Signed in — local daemon is offline. Refresh or finish Connect."
		connActions = append(connActions, outlineButton("Open Connect", func() { s.switchSection(secConnect) }))
	} else if st.Connected {
		connBody = fmt.Sprintf("%s · %s", first(st.Username, st.UserID, "account"), orDash(st.ServerURL))
	} else {
		connBody = "Signed in — linking workspace to the portal…"
	}
	cards = append(cards, card("Connection", connBody, connActions...))

	harvestBody := "Daemon offline — harvest pulse unavailable."
	var harvestActions []fyne.CanvasObject
	if h != nil {
		harvestBody = fmt.Sprintf("Last scan %s · files/turns %d/%d\nActive sessions %d · saved/errors %d/%d",
			orDash(h.LastScanAt), h.LastScanFiles, h.LastScanTurns,
			h.ActiveSessions, h.ProposalsSaved, h.ProposalErrors)
		if msg := strings.TrimSpace(h.Message); msg != "" {
			harvestBody += "\n" + msg
		}
		harvestActions = append(harvestActions, outlineButton("Open Harvest", func() { s.switchSection(secHarvest) }))
	} else if signedIn {
		harvestActions = append(harvestActions, outlineButton("Open Connect", func() { s.switchSection(secConnect) }))
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
