package desktopui

import (
	"fmt"
	"path/filepath"
	"strings"

	"central-memory/internal/cloudclient"
	"central-memory/internal/localclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// previewTurn is one chat-style block in the right pane.
type previewTurn struct {
	role string
	meta string
	body string
}

func (s *Shell) setPreview(title, body string) {
	s.setPreviewKind("system", title, body)
}

func (s *Shell) setPreviewKind(kind, title, body string) {
	s.setPreviewContent(kind, title, body, true)
}

func (s *Shell) setPreviewContent(kind, title, body string, clearLinks bool) {
	s.mu.Lock()
	s.previewKind = kind
	s.mu.Unlock()
	if s.previewHead != nil {
		s.previewHead.SetText(title)
	}
	if clearLinks && s.previewLinks != nil {
		s.previewLinks.Objects = nil
		s.previewLinks.Hide()
		s.previewLinks.Refresh()
	}
	s.renderPreviewPlain(body, false)
}

func (s *Shell) showMemoryPreview(it cloudclient.MemoryItem) {
	s.mu.Lock()
	s.previewKind = "memory"
	root := ""
	if s.cachedStatus != nil {
		root = s.cachedStatus.Root
	}
	s.mu.Unlock()

	title := "Memory · " + first(it.Key, it.ID, "entry")
	if s.previewHead != nil {
		s.previewHead.SetText(title)
	}

	meta := formatMemoryMeta(it)
	content := strings.TrimSpace(it.Content)
	if snip := strings.TrimSpace(it.ContextSnippet); snip != "" {
		content = "Context:\n" + snip + "\n\n" + content
	}
	s.renderPreviewTurns(meta, []previewTurn{{
		role: "MEMORY",
		meta: first(it.Status, it.Level, "entry"),
		body: content,
	}}, false)

	refs := memoryFileRefs(it)
	if s.previewLinks == nil {
		return
	}
	objs := []fyne.CanvasObject{sectionHeading("Linked files")}
	if len(refs) == 0 {
		objs = append(objs, mutedLabel("No file references found in this memory."))
	} else {
		objs = append(objs, mutedLabel("Open a path in this preview pane:"))
		for _, p := range refs {
			p := p
			label := filepath.Base(p)
			if label == "" || label == "." {
				label = p
			} else {
				label = label + "  ·  " + truncate(p, 48)
			}
			btn := widget.NewButtonWithIcon(label, theme.DocumentIcon(), func() {
				s.openMemoryFile(p, root)
			})
			btn.Alignment = widget.ButtonAlignLeading
			objs = append(objs, btn)
		}
	}
	s.previewLinks.Objects = objs
	s.previewLinks.Show()
	s.previewLinks.Refresh()
}

func (s *Shell) openMemoryFile(path, workspaceRoot string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	candidates := readPathCandidates(path, workspaceRoot)
	title := "File · " + filepath.Base(path)

	go func() {
		var lastErr error
		var tried []string
		for _, readPath := range candidates {
			if readPath == "" {
				continue
			}
			tried = append(tried, readPath)
			fr, err := s.client.ReadFile(readPath)
			if err == nil {
				body := fr.Content
				if len(body) > 120_000 {
					body = body[:120_000] + "\n\n… truncated …"
				}
				fyne.Do(func() {
					s.mu.Lock()
					s.previewKind = "memory"
					s.mu.Unlock()
					if s.previewHead != nil {
						s.previewHead.SetText(title)
					}
					meta := fmt.Sprintf("Path: %s · %d bytes", fr.Path, fr.Size)
					s.renderPreviewTurns(meta, []previewTurn{{
						role: "FILE",
						meta: filepath.Base(fr.Path),
						body: body,
					}}, false)
				})
				return
			}
			lastErr = err
		}
		fyne.Do(func() {
			msg := "Path: " + path + "\nWorkspace: " + orDash(workspaceRoot) + "\n"
			if len(tried) > 0 {
				msg += "Tried: " + strings.Join(tried, " · ") + "\n"
			}
			msg += "\nCould not read via daemon:\n" + errString(lastErr)
			msg += "\n\nDaemon reads are sandboxed to the workspace root (relative paths via SecureJoin)."
			msg += " If this file moved or lived only in a memory note, pick the matching folder on Home/Workspace."
			s.setPreviewKind("memory", title, msg)
		})
	}()
}

func errString(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func (s *Shell) showHarnessPreview(row harvestRow, h *localclient.Harvest) {
	files := harvestFilesForAgent(h, row.agent)
	header := row.detail
	if len(files) == 0 {
		s.setPreviewKind("harvest", "Harness · "+row.title, header+"\n\nNo transcript files in the latest scan list yet. Run Scan on Home, then select a file row when it appears.")
		if s.previewLinks != nil {
			s.previewLinks.Objects = nil
			s.previewLinks.Hide()
			s.previewLinks.Refresh()
		}
		return
	}

	// File picker buttons in the preview pane.
	if s.previewLinks != nil {
		objs := []fyne.CanvasObject{sectionHeading("Transcripts · " + row.title)}
		for _, f := range files {
			f := f
			label := first(f.Name, filepath.Base(f.Path))
			fmtLabel := first(f.Format, "file")
			btn := widget.NewButtonWithIcon(label+"  ·  "+fmtLabel, theme.DocumentIcon(), func() {
				s.loadHarvestTranscript(f.Path, label, f.Format)
			})
			btn.Alignment = widget.ButtonAlignLeading
			objs = append(objs, btn)
		}
		s.previewLinks.Objects = objs
		s.previewLinks.Show()
		s.previewLinks.Refresh()
	}

	// Auto-load newest JSONL (prefer jsonl over sqlite).
	pick := files[0]
	for _, f := range files {
		if strings.EqualFold(f.Format, "jsonl") || strings.HasSuffix(strings.ToLower(f.Path), ".jsonl") {
			pick = f
			break
		}
	}
	s.setPreviewContent("harvest", "Harness · "+row.title, "Loading transcript…\n\n"+header, false)
	s.loadHarvestTranscript(pick.Path, first(pick.Name, filepath.Base(pick.Path)), pick.Format)
}

func (s *Shell) loadHarvestTranscript(path, title, format string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "sqlite" || strings.HasSuffix(strings.ToLower(path), ".db") {
		s.setPreviewKind("harvest", "Transcript · "+title,
			"Path: "+path+"\n\nSQLite agent DBs are harvested for turns but not rendered as a conversation preview yet.\nPick a .jsonl transcript when available.")
		return
	}
	go func() {
		fr, err := s.client.ReadHarvestFile(path)
		fyne.Do(func() {
			if err != nil {
				s.setPreviewContent("harvest", "Transcript · "+title,
					"Path: "+path+"\n\nCould not read harvest transcript:\n"+err.Error()+
						"\n\nTranscripts live outside the workspace; Desktop uses /local/harvest/read with an allowlist from the latest scan.",
					false)
				return
			}
			s.mu.Lock()
			s.previewKind = "harvest"
			s.mu.Unlock()
			if s.previewHead != nil {
				s.previewHead.SetText("Transcript · " + title)
			}
			meta := fmt.Sprintf("Path: %s · %d bytes", fr.Path, fr.Size)
			if fr.TurnCount > 0 {
				meta += fmt.Sprintf(" · %d turns", fr.TurnCount)
			}
			if len(fr.Turns) > 0 {
				turns := make([]previewTurn, 0, len(fr.Turns))
				for _, t := range fr.Turns {
					turns = append(turns, previewTurn{
						role: first(t.Speaker, "UNKNOWN"),
						meta: t.Timestamp,
						body: t.Content,
					})
				}
				s.renderPreviewTurns(meta, turns, true)
				return
			}
			body := strings.TrimSpace(fr.Formatted)
			if body == "" {
				body = fr.Content
			}
			if len(body) > 120_000 {
				body = body[:120_000] + "\n\n… truncated …"
			}
			s.renderPreviewTurns(meta, []previewTurn{{
				role: "TRANSCRIPT",
				meta: title,
				body: body,
			}}, true)
		})
	}()
}

func previewEmptyMessage(sec section) string {
	switch sec {
	case secMemory:
		return "Select an item to preview\n\nBrowse or search, then select a memory for full details and linked files."
	case secHarvest:
		return "Select an item to preview\n\nSelect a harness or transcript to inspect it here."
	case secWorkspace:
		return "Select an item to preview\n\nSelect a workspace file or choose a folder. Agent transcripts live under Harvest."
	case secSessions:
		return "Select a snapshot to preview\n\nRestore hydrates harness transcript + git diff into your linked workspace."
	default:
		return "Select an item to preview"
	}
}

func (s *Shell) clearPreviewForSection(sec section) {
	s.mu.Lock()
	kind := s.previewKind
	s.mu.Unlock()

	keep := false
	switch sec {
	case secMemory:
		keep = kind == "memory"
	case secHarvest:
		keep = kind == "harvest"
	case secWorkspace:
		keep = kind == "workspace"
	case secSessions:
		keep = kind == "sessions"
	default:
		keep = false
	}
	if keep {
		return
	}
	s.setPreviewKind("", "Preview", previewEmptyMessage(sec))
}

func (s *Shell) buildPreviewPane() fyne.CanvasObject {
	if s.previewLinks == nil {
		s.previewLinks = container.NewVBox()
		s.previewLinks.Hide()
	}
	if s.previewMeta == nil {
		s.previewMeta = mutedLabel("")
		s.previewMeta.Hide()
	}
	if s.previewTurns == nil {
		s.previewTurns = container.NewVBox()
	}
	scrollBody := container.NewVBox(s.previewLinks, s.previewMeta, s.previewTurns)
	s.previewScroll = container.NewVScroll(scrollBody)
	// Cap scroll min height so long transcripts grow inside the pane, not the window.
	s.previewScroll.SetMinSize(fyne.NewSize(previewMinW-24, 280))

	inner := container.NewBorder(
		container.NewVBox(container.NewPadded(s.previewHead), widget.NewSeparator()),
		nil, nil, nil,
		container.NewPadded(s.previewScroll),
	)
	return withMinWidth(previewMinW, paneBG(inner, colorSurface))
}

func (s *Shell) renderPreviewPlain(body string, scrollToEnd bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		body = " "
	}
	s.renderPreviewTurns("", []previewTurn{{
		role: "",
		body: body,
	}}, scrollToEnd)
}

func (s *Shell) renderPreviewTurns(meta string, turns []previewTurn, scrollToEnd bool) {
	if s.previewMeta != nil {
		if strings.TrimSpace(meta) == "" {
			s.previewMeta.Hide()
			s.previewMeta.SetText("")
		} else {
			s.previewMeta.SetText(meta)
			s.previewMeta.Show()
		}
		s.previewMeta.Refresh()
	}
	if s.previewTurns == nil {
		return
	}
	objs := make([]fyne.CanvasObject, 0, len(turns))
	for _, t := range turns {
		objs = append(objs, chatTurnBlock(t))
	}
	if len(objs) == 0 {
		objs = append(objs, mutedLabel("(empty)"))
	}
	s.previewTurns.Objects = objs
	s.previewTurns.Refresh()
	if s.previewScroll != nil {
		s.previewScroll.Refresh()
		if scrollToEnd {
			s.previewScroll.ScrollToBottom()
		} else {
			s.previewScroll.Offset = fyne.NewPos(0, 0)
			s.previewScroll.Refresh()
		}
	}
}

func chatTurnBlock(t previewTurn) fyne.CanvasObject {
	body := widget.NewLabel(t.body)
	body.Wrapping = fyne.TextWrapWord
	if strings.TrimSpace(t.role) == "" {
		return container.NewPadded(body)
	}
	head := t.role
	if strings.TrimSpace(t.meta) != "" {
		head = t.role + "  ·  " + t.meta
	}
	role := widget.NewLabelWithStyle(head, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	role.SizeName = theme.SizeNameCaptionText
	return cardWrap(container.NewVBox(role, body))
}

func formatMemoryMeta(it cloudclient.MemoryItem) string {
	parts := []string{}
	if it.Level != "" || it.Scope != "" {
		parts = append(parts, "Level "+orDash(it.Level)+" · Scope "+orDash(it.Scope))
	}
	if it.Status != "" {
		parts = append(parts, it.Status)
	}
	if it.Category != "" {
		parts = append(parts, it.Category)
	}
	if len(it.Tags) > 0 {
		parts = append(parts, "tags: "+strings.Join(it.Tags, ", "))
	}
	if it.UpdatedAt != "" {
		parts = append(parts, "updated "+it.UpdatedAt)
	}
	return strings.Join(parts, " · ")
}

func (s *Shell) loadFilePreview(path, title string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	kind := "harvest"
	if strings.HasPrefix(title, "Workspace") {
		kind = "workspace"
	}
	go func() {
		fr, err := s.client.ReadFile(path)
		fyne.Do(func() {
			if err != nil {
				s.setPreviewKind(kind, title, "Path: "+path+"\n\nCould not read via daemon (workspace-relative paths only):\n"+err.Error())
				return
			}
			body := fr.Content
			if len(body) > 120_000 {
				body = body[:120_000] + "\n\n… truncated …"
			}
			s.mu.Lock()
			s.previewKind = kind
			s.mu.Unlock()
			if s.previewHead != nil {
				s.previewHead.SetText(title)
			}
			meta := fmt.Sprintf("Path: %s · %d bytes", fr.Path, fr.Size)
			s.renderPreviewTurns(meta, []previewTurn{{
				role: "FILE",
				meta: filepath.Base(fr.Path),
				body: body,
			}}, false)
		})
	}()
}

func (s *Shell) clearPreviewHint(msg string) {
	s.setPreviewKind("system", "Preview", msg)
}
