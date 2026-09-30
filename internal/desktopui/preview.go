package desktopui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
)

func (s *Shell) setPreview(title, body string) {
	s.setPreviewKind("system", title, body)
}

func (s *Shell) setPreviewKind(kind, title, body string) {
	s.mu.Lock()
	s.previewKind = kind
	s.mu.Unlock()
	s.previewHead.SetText(title)
	s.previewBody.SetText(body)
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
	case secHome, secConnect, secSettings:
		keep = kind == "system"
	}
	if keep {
		return
	}
	switch sec {
	case secMemory:
		s.setPreviewKind("", "Preview", "Search and select a memory entry to inspect it here.")
	case secHarvest:
		s.setPreviewKind("", "Preview", "Select a harness or transcript to inspect it here.")
	case secWorkspace:
		s.setPreviewKind("", "Preview", "Select a workspace file or choose a folder. Agent transcripts live under Harvest.")
	default:
		s.setPreviewKind("", "Preview", "Select a memory entry, harvest transcript, or workspace file to inspect content here.")
	}
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
