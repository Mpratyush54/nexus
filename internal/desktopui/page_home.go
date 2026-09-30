package desktopui

import (
	"fmt"
	"strings"

	"central-memory/internal/cloudclient"
	"central-memory/internal/localclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// homeWidgets holds durable Home dashboard controls updated in place on Refresh
// so the scroll container is not recreated (avoids jump-to-top).
type homeWidgets struct {
	root fyne.CanvasObject

	projectTitle *widget.Label
	projectSub   *widget.Label

	memoriesVal *widget.Label
	memoriesSub *widget.Label
	harvestVal  *widget.Label
	harvestSub  *widget.Label
	teamVal     *widget.Label
	teamSub     *widget.Label
	agentsVal   *widget.Label
	agentsSub   *widget.Label

	heatmap *activityHeatmap

	pulseBody   *widget.Label
	connectBody *widget.Label
	connectActs *fyne.Container
	setupBox    *fyne.Container
	recentBox   *fyne.Container
	nextBody    *widget.Label
	nextActs    *fyne.Container
}

func (s *Shell) homePage() fyne.CanvasObject {
	if s.homeUI != nil && s.homeUI.root != nil {
		return s.homeUI.root
	}

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
		pageHeader("Home", "Project pulse, activity, and a compact setup strip."),
	)

	hw := &homeWidgets{
		projectTitle: widget.NewLabelWithStyle("Working on", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		projectSub:   mutedLabel("Link a workspace so harvest sets the active project."),
		memoriesVal:  widget.NewLabelWithStyle("—", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		memoriesSub:  mutedLabel("Memories"),
		harvestVal:   widget.NewLabelWithStyle("—", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		harvestSub:   mutedLabel("Harvest saved"),
		teamVal:      widget.NewLabelWithStyle("—", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		teamSub:      mutedLabel("Team"),
		agentsVal:    widget.NewLabelWithStyle("—", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		agentsSub:    mutedLabel("Agent calls"),
		heatmap:      newActivityHeatmap(),
		pulseBody:    mutedLabel("Waiting for harvest pulse…"),
		connectBody:  mutedLabel("Sign in to link your cloud account."),
		connectActs:  container.NewHBox(),
		setupBox:     container.NewVBox(),
		recentBox:    container.NewVBox(),
		nextBody:     mutedLabel("Sign in to get started."),
		nextActs:     container.NewHBox(),
	}

	stats := container.NewGridWithColumns(4,
		statTile(hw.memoriesVal, hw.memoriesSub),
		statTile(hw.harvestVal, hw.harvestSub),
		statTile(hw.teamVal, hw.teamSub),
		statTile(hw.agentsVal, hw.agentsSub),
	)

	connectStrip := raisedPanel(container.NewVBox(
		widget.NewLabelWithStyle("Connection", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		hw.connectBody,
		hw.connectActs,
	))

	pulseCard := raisedPanel(container.NewVBox(
		widget.NewLabelWithStyle("Scanner", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		hw.pulseBody,
	))

	nextCard := raisedPanel(container.NewVBox(
		widget.NewLabelWithStyle("Next", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		hw.nextBody,
		hw.nextActs,
	))

	setupCard := raisedPanel(container.NewVBox(
		sectionHeading("Setup"),
		mutedLabel("Finish these once — then Home stays a dashboard."),
		hw.setupBox,
	))

	recentCard := raisedPanel(container.NewVBox(
		sectionHeading("Recent workspaces"),
		hw.recentBox,
	))

	body := container.NewVBox(
		hw.projectTitle,
		hw.projectSub,
		widget.NewSeparator(),
		stats,
		widget.NewSeparator(),
		raisedPanel(hw.heatmap.canvasObject()),
		widget.NewSeparator(),
		connectStrip,
		pulseCard,
		nextCard,
		widget.NewSeparator(),
		setupCard,
		recentCard,
	)

	scroll := container.NewScroll(container.NewPadded(body))
	root := container.NewBorder(
		container.NewVBox(header, widget.NewSeparator()),
		nil, nil, nil,
		scroll,
	)
	hw.root = root
	s.homeUI = hw
	s.homeBody = body

	s.mu.Lock()
	st := s.cachedStatus
	h := s.cachedHarvest
	signedIn := s.signedIn
	recent := append([]string(nil), s.recentWorkspaces...)
	dash := s.cachedDashboard
	s.mu.Unlock()
	s.updateHomeDashboard(st, h, signedIn, recent, dash)

	return root
}

func statTile(value, caption *widget.Label) fyne.CanvasObject {
	return raisedPanel(container.NewVBox(caption, value))
}

func raisedPanel(inner fyne.CanvasObject) fyne.CanvasObject {
	return cardWrap(inner)
}

// updateHomeDashboard mutates Home widgets in place — never replaces the scroll root.
func (s *Shell) updateHomeDashboard(st *localclient.Status, h *localclient.Harvest, signedIn bool, recent []string, dash *cloudclient.ProjectDashboard) {
	if s.homeUI == nil {
		return
	}
	hw := s.homeUI

	pid := memoryProjectID(st, h)
	projectLabel := "No linked project"
	if st != nil && strings.TrimSpace(st.Root) != "" {
		projectLabel = recentDisplayName(st.Root)
		if i := strings.Index(projectLabel, " — "); i > 0 {
			projectLabel = projectLabel[:i]
		}
	}
	hw.projectTitle.SetText(projectLabel)
	if pid != "" {
		hw.projectSub.SetText("Project " + pid)
	} else if signedIn {
		hw.projectSub.SetText("Memories are per project — finish workspace + harvest to link one.")
	} else {
		hw.projectSub.SetText("Sign in to see cloud dashboard metrics.")
	}

	if dash != nil {
		m := dash.Metrics
		hw.memoriesVal.SetText(fmt.Sprintf("%d", m.Memories))
		hw.memoriesSub.SetText(fmt.Sprintf("%d confirmed · %d proposed", m.Confirmed, m.Proposed))
		hw.teamVal.SetText(fmt.Sprintf("%d", m.Members))
		hw.teamSub.SetText(fmt.Sprintf("%d online", m.Online))
		hw.agentsVal.SetText(fmt.Sprintf("%d", m.AgentCalls))
		hw.agentsSub.SetText("MCP tool calls")
		hw.heatmap.setDays(dash.Heatmap)
	} else {
		hw.memoriesVal.SetText("—")
		hw.memoriesSub.SetText("Memories")
		hw.teamVal.SetText("—")
		hw.teamSub.SetText("Team")
		hw.agentsVal.SetText("—")
		hw.agentsSub.SetText("Agent calls")
		hw.heatmap.setDays(nil)
	}
	if h != nil {
		hw.harvestVal.SetText(fmt.Sprintf("%d", h.ProposalsSaved))
		hw.harvestSub.SetText(fmt.Sprintf("Last scan %s", orDash(shortRel(h.LastScanAt))))
	} else {
		hw.harvestVal.SetText("—")
		hw.harvestSub.SetText("Desktop offline")
	}

	connBody := "Sign in to link your cloud account."
	hw.connectActs.Objects = nil
	if !signedIn {
		hw.connectActs.Add(primaryButton("Sign in", func() {
			if s.hooks.OnSignIn != nil {
				s.hooks.OnSignIn()
			}
		}))
	} else if st == nil {
		connBody = "Signed in — local daemon is offline. Refresh to start it."
		hw.connectActs.Add(outlineButton("Refresh", func() {
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
	hw.connectBody.SetText(connBody)
	hw.connectActs.Refresh()

	harvestBody := "Daemon offline — harvest pulse unavailable."
	if h != nil {
		harvestBody = fmt.Sprintf("Last scan %s · files/turns %d/%d\nActive sessions %d · saved/errors %d/%d",
			orDash(h.LastScanAt), h.LastScanFiles, h.LastScanTurns,
			h.ActiveSessions, h.ProposalsSaved, h.ProposalErrors)
		if msg := strings.TrimSpace(h.Message); msg != "" {
			harvestBody += "\n" + msg
		}
		if p := strings.TrimSpace(h.ProjectID); p != "" {
			harvestBody += "\nProject: " + p
		}
		if dash != nil && len(dash.Recent) > 0 {
			harvestBody += "\n\nRecent cloud events:"
			limit := 5
			if len(dash.Recent) < limit {
				limit = len(dash.Recent)
			}
			for _, ev := range dash.Recent[:limit] {
				harvestBody += fmt.Sprintf("\n· %s  %s", first(ev.EventType, "event"), shortRel(ev.CreatedAt))
			}
		}
	}
	hw.pulseBody.SetText(harvestBody)

	hw.nextActs.Objects = nil
	nextBody := "Sign in to get started."
	switch {
	case !signedIn:
		hw.nextActs.Add(primaryButton("Sign in", func() {
			if s.hooks.OnSignIn != nil {
				s.hooks.OnSignIn()
			}
		}))
	case st == nil || strings.TrimSpace(st.Root) == "":
		nextBody = "Choose a workspace folder so the daemon can watch agent transcripts."
		hw.nextActs.Add(primaryButton("Choose folder", func() { s.pickWorkspaceFolder() }))
	case h == nil || (h.LastScanFiles == 0 && len(h.Agents) == 0):
		nextBody = "Workspace is set. Run a harvest scan to pick up agent transcripts."
		hw.nextActs.Add(primaryButton("Scan now", func() {
			_ = s.client.TriggerHarvest()
			s.Refresh()
		}))
	default:
		nextBody = "Memory = cloud facts · Harvest = transcripts · Workspace = repo files."
		hw.nextActs.Add(outlineButton("Memory", func() { s.switchSection(secMemory) }))
		hw.nextActs.Add(outlineButton("Harvest", func() { s.switchSection(secHarvest) }))
	}
	hw.nextBody.SetText(nextBody)
	hw.nextActs.Refresh()

	hw.setupBox.Objects = nil
	for _, it := range buildConnectChecklist(st, h, signedIn) {
		it := it
		mark := checklistMark(it.done)
		row := container.NewBorder(nil, nil, widget.NewLabel(mark+"  "+it.title), nil, mutedLabel(it.detail))
		var acts []fyne.CanvasObject
		if it.signIn {
			acts = append(acts, primaryButton(it.action, func() {
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
			acts = append(acts, secondaryButton(label, func() { s.pickWorkspaceFolder() }))
		}
		if it.goTo == secHarvest && it.done {
			acts = append(acts, outlineButton("Open Harvest", func() { s.switchSection(secHarvest) }))
		}
		if len(acts) > 0 {
			hw.setupBox.Add(container.NewVBox(row, container.NewHBox(acts...)))
		} else {
			hw.setupBox.Add(row)
		}
	}
	hw.setupBox.Refresh()

	hw.recentBox.Objects = nil
	if len(recent) == 0 {
		hw.recentBox.Add(mutedLabel("No recent folders yet."))
	} else {
		for _, path := range recent {
			path := path
			btn := outlineButton(recentDisplayName(path), func() {
				s.selectWorkspacePath(path)
			})
			btn.Alignment = widget.ButtonAlignLeading
			hw.recentBox.Add(btn)
		}
	}
	hw.recentBox.Refresh()
}

// rebuildHomeCards updates the durable Home tree (alias kept for Refresh call sites).
func (s *Shell) rebuildHomeCards(st *localclient.Status, h *localclient.Harvest, signedIn bool, recent ...[]string) {
	var recentList []string
	if len(recent) > 0 {
		recentList = recent[0]
	} else {
		s.mu.Lock()
		recentList = append([]string(nil), s.recentWorkspaces...)
		s.mu.Unlock()
	}
	s.mu.Lock()
	dash := s.cachedDashboard
	s.mu.Unlock()
	if s.homeUI == nil {
		_ = s.homePage()
		return
	}
	s.updateHomeDashboard(st, h, signedIn, recentList, dash)
}

func shortRel(iso string) string {
	iso = strings.TrimSpace(iso)
	if iso == "" {
		return "—"
	}
	if len(iso) >= 16 {
		return iso[:16]
	}
	return iso
}
