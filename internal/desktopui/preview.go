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
	if s.previewBody != nil {
		s.previewBody.SetText(body)
		s.previewBody.Show()
	}
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
	if s.previewBody != nil {
		s.previewBody.SetText(formatMemoryDetail(it))
		s.previewBody.Show()
	}

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
					s.setPreviewKind("memory", title, "Path: "+fr.Path+"\nSize: "+fmt.Sprintf("%d", fr.Size)+" bytes\n\n"+body)
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
	s.loadHarvestTranscript(pick.Path, first(pick.Name, filepath.Base(pick.Path)), pick.Format)
	s.setPreviewContent("harvest", "Harness · "+row.title, "Loading transcript…\n\n"+header, false)
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
			body := strings.TrimSpace(fr.Formatted)
			if body == "" {
				body = fr.Content
			}
			if len(body) > 120_000 {
				body = body[:120_000] + "\n\n… truncated …"
			}
			head := "Path: " + fr.Path + "\nSize: " + fmt.Sprintf("%d", fr.Size) + " bytes\n\n"
			s.setPreviewContent("harvest", "Transcript · "+title, head+body, false)
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
	scrollBody := container.NewVBox(s.previewLinks, s.previewBody)
	inner := container.NewBorder(
		container.NewVBox(container.NewPadded(s.previewHead), widget.NewSeparator()),
		nil, nil, nil,
		container.NewPadded(container.NewScroll(scrollBody)),
	)
	return paneBG(inner, colorSurface)
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
			s.setPreviewKind(kind, title, "Path: "+fr.Path+"\nSize: "+fmt.Sprintf("%d", fr.Size)+" bytes\n\n"+body)
		})
	}()
}

func (s *Shell) clearPreviewHint(msg string) {
	s.setPreviewKind("system", "Preview", msg)
}
