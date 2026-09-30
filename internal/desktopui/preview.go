package desktopui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
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
	if s.previewBody != nil {
		s.previewBody.SetText(body)
	}
}

func previewEmptyMessage(sec section) string {
	switch sec {
	case secMemory:
		return "Select an item to preview\n\nSearch and select a memory entry to inspect it here."
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
		// Home / Connect / Settings — drop list preview so it never goes stale across pages.
		keep = false
	}
	if keep {
		return
	}
	s.mu.Lock()
	s.previewKind = ""
	s.mu.Unlock()
	if s.previewHead != nil {
		s.previewHead.SetText("Preview")
	}
	if s.previewBody != nil {
		s.previewBody.SetText(previewEmptyMessage(sec))
	}
}

func (s *Shell) buildPreviewPane() fyne.CanvasObject {
	inner := container.NewBorder(
		container.NewVBox(container.NewPadded(s.previewHead), widget.NewSeparator()),
		nil, nil, nil,
		container.NewPadded(container.NewScroll(s.previewBody)),
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
