package desktopui

import (
	"fmt"
	"path/filepath"
	"strings"

	"central-memory/internal/cloudclient"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) setPreview(title, body string) {
	s.setPreviewKind("system", title, body)
}

func (s *Shell) setPreviewKind(kind, title, body string) {
	s.mu.Lock()
	s.previewKind = kind
	s.mu.Unlock()
	if s.previewHead != nil {
		s.previewHead.SetText(title)
	}
	if s.previewLinks != nil {
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
	readPath := resolveReadPath(path, workspaceRoot)
	title := "File · " + filepath.Base(path)
	outside := filepath.IsAbs(path) && readPath == path && workspaceRoot != "" &&
		!strings.HasPrefix(strings.ToLower(filepath.Clean(path)), strings.ToLower(filepath.Clean(workspaceRoot))+string(filepath.Separator)) &&
		!strings.EqualFold(filepath.Clean(path), filepath.Clean(workspaceRoot))

	go func() {
		fr, err := s.client.ReadFile(readPath)
		fyne.Do(func() {
			if err != nil {
				msg := "Path: " + path + "\n"
				if readPath != path {
					msg += "Daemon path: " + readPath + "\n"
				}
				msg += "\nCould not read via daemon:\n" + err.Error()
				if outside {
					msg += "\n\nThis path looks outside the current workspace. Choose that folder on Home/Workspace, or copy the path above."
				}
				s.setPreviewKind("memory", title, msg)
				return
			}
			body := fr.Content
			if len(body) > 120_000 {
				body = body[:120_000] + "\n\n… truncated …"
			}
			s.setPreviewKind("memory", title, "Path: "+fr.Path+"\nSize: "+fmt.Sprintf("%d", fr.Size)+" bytes\n\n"+body)
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
