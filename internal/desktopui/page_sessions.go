package desktopui

import (
	"fmt"
	"strings"
	"time"

	"central-memory/internal/localclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) sessionsPage() fyne.CanvasObject {
	header := pageHeader("Sessions", "Cloud session snapshots (Teleport) — restore harness transcript + git diff onto this workspace.")
	refresh := secondaryButton("Refresh", func() {
		go s.loadSessionSnapshots()
	})
	refresh.Icon = theme.ViewRefreshIcon()

	s.mu.Lock()
	needLoad := len(s.sessionRows) == 0 && s.signedIn
	s.mu.Unlock()
	if needLoad {
		go s.loadSessionSnapshots()
	}

	return container.NewBorder(
		container.NewVBox(
			header,
			container.NewHBox(refresh),
			s.sessionBanner,
			widget.NewSeparator(),
		),
		nil, nil, nil,
		s.sessionList,
	)
}

func (s *Shell) setSessionBanner(msg string, cta string, onCTA func()) {
	if s.sessionBanner == nil {
		return
	}
	if strings.TrimSpace(msg) == "" {
		s.sessionBanner.Objects = nil
		s.sessionBanner.Refresh()
		return
	}
	objs := []fyne.CanvasObject{mutedLabel(msg)}
	if cta != "" && onCTA != nil {
		objs = append(objs, outlineButton(cta, onCTA))
	}
	s.sessionBanner.Objects = []fyne.CanvasObject{container.NewVBox(objs...)}
	s.sessionBanner.Refresh()
}

func (s *Shell) loadSessionSnapshots() {
	s.mu.Lock()
	signedIn := s.signedIn
	pid := memoryProjectID(s.cachedStatus, s.cachedHarvest)
	s.mu.Unlock()

	if !signedIn {
		fyne.Do(func() {
			s.setSessionBanner("Sign in to list cloud session snapshots.", "Sign in", func() {
				if s.hooks.OnSignIn != nil {
					s.hooks.OnSignIn()
				}
			})
			s.setPreviewKind("sessions", "Sessions", "Not signed in.")
		})
		return
	}

	items, err := s.client.ListSnapshots(pid)
	fyne.Do(func() {
		if err != nil {
			s.setSessionBanner("Could not load snapshots (daemon :7272): "+err.Error(), "Open Home", func() {
				s.switchSection(secHome)
			})
			s.setPreviewKind("sessions", "Sessions", err.Error()+"\n\nWeb portal: /app/sessions")
			return
		}
		rows := make([]sessionRow, 0, len(items))
		for _, it := range items {
			rows = append(rows, snapshotToRow(it))
		}
		s.mu.Lock()
		s.sessionRows = rows
		s.mu.Unlock()
		s.sessionList.Refresh()
		if len(rows) == 0 {
			s.setSessionBanner("No snapshots yet. Desktop pushes them while you work; also listed on web Sessions (/app/sessions).", "", nil)
			s.setPreviewKind("sessions", "Sessions", "No cloud snapshots for this project yet.\n\nWeb: /app/sessions · CLI: nexus session restore <id> --workspace <path>")
			return
		}
		s.setSessionBanner(fmt.Sprintf("%d snapshot(s). Select one to restore into the linked workspace.", len(rows)), "", nil)
		s.setPreviewKind("sessions", "Sessions", "Select a snapshot to preview restore options.\n\nAlso on web: /app/sessions")
	})
}

func snapshotToRow(it localclient.SessionSnapshot) sessionRow {
	sid := strings.TrimSpace(it.SessionID)
	short := sid
	if len(short) > 8 {
		short = short[:8] + "…"
	}
	age := ""
	if it.AgeSeconds > 0 {
		age = formatAge(it.AgeSeconds)
	} else if it.UpdatedAt != "" {
		age = it.UpdatedAt
	}
	title := first(it.Harness, "session") + " · " + short
	sub := fmt.Sprintf("%d turns", it.TurnCount)
	if it.GitBranch != "" {
		sub += " · " + it.GitBranch
	}
	if age != "" {
		sub += " · " + age
	}
	detail := fmt.Sprintf("Session: %s\nHarness: %s\nTurns: %d\nBranch: %s\nMachine: %s\nUpdated: %s\n\nRestore writes transcript + uncommitted diff into the linked workspace (git checkout may run).",
		sid, orDash(it.Harness), it.TurnCount, orDash(it.GitBranch), orDash(it.SourceMachineID), orDash(it.UpdatedAt))
	return sessionRow{
		sessionID: sid,
		title:     title,
		subtitle:  sub,
		detail:    detail,
		harness:   it.Harness,
		branch:    it.GitBranch,
		machine:   it.SourceMachineID,
	}
}

func formatAge(sec int) string {
	if sec < 60 {
		return fmt.Sprintf("%ds ago", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm ago", sec/60)
	}
	if sec < 86400 {
		return fmt.Sprintf("%dh ago", sec/3600)
	}
	return fmt.Sprintf("%dd ago", sec/86400)
}

func (s *Shell) showSessionPreview(row sessionRow) {
	s.mu.Lock()
	s.previewKind = "sessions"
	root := ""
	if s.cachedStatus != nil {
		root = s.cachedStatus.Root
	}
	s.mu.Unlock()

	if s.previewHead != nil {
		s.previewHead.SetText("Snapshot · " + row.title)
	}
	s.renderPreviewTurns("", []previewTurn{{
		role: "SNAPSHOT",
		meta: row.sessionID,
		body: row.detail,
	}}, false)

	if s.previewLinks == nil {
		return
	}
	cli := fmt.Sprintf("nexus session restore %s --workspace %q", row.sessionID, root)
	objs := []fyne.CanvasObject{
		sectionHeading("Teleport actions"),
		mutedLabel("Restore hydrates this snapshot onto the linked workspace folder."),
		primaryButton("Restore to workspace", func() {
			s.restoreSession(row.sessionID, root, false)
		}),
		outlineButton("Force restore", func() {
			s.restoreSession(row.sessionID, root, true)
		}),
		outlineButton("Copy CLI command", func() {
			if clip := s.win.Clipboard(); clip != nil {
				clip.SetContent(cli)
			}
			s.setPreviewKind("sessions", "Snapshot · "+row.title, row.detail+"\n\nCopied:\n"+cli)
		}),
	}
	if root == "" {
		objs = append([]fyne.CanvasObject{mutedLabel("Choose a workspace on Home before restoring.")}, objs...)
	}
	s.previewLinks.Objects = objs
	s.previewLinks.Show()
	s.previewLinks.Refresh()
}

func (s *Shell) restoreSession(sessionID, workspace string, force bool) {
	sessionID = strings.TrimSpace(sessionID)
	workspace = strings.TrimSpace(workspace)
	if sessionID == "" {
		return
	}
	if workspace == "" {
		s.setPreviewKind("sessions", "Restore", "Workspace folder required — pick one on Home or Workspace first.")
		return
	}
	s.setPreviewKind("sessions", "Restoring…", "Downloading snapshot and applying to:\n"+workspace+"\n\nThis can take a minute.")
	go func() {
		msg, err := s.client.RestoreSession(sessionID, workspace, force)
		fyne.Do(func() {
			if err != nil {
				s.setPreviewKind("sessions", "Restore failed", err.Error()+"\n\nCLI fallback:\nnexus session restore "+sessionID+" --workspace "+fmt.Sprintf("%q", workspace))
				return
			}
			if msg == "" {
				msg = "Restore completed at " + time.Now().Format(time.RFC3339)
			}
			s.setPreviewKind("sessions", "Restored", msg)
		})
	}()
}
