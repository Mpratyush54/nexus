package desktopui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
)

func (s *Shell) setPreview(title, body string) {
	s.previewHead.SetText(title)
	s.previewBody.SetText(body)
}

func (s *Shell) loadFilePreview(path, title string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	go func() {
		fr, err := s.client.ReadFile(path)
		fyne.Do(func() {
			if err != nil {
				s.setPreview(title, "Path: "+path+"\n\nCould not read via daemon (workspace-relative paths only):\n"+err.Error())
				return
			}
			body := fr.Content
			if len(body) > 120_000 {
				body = body[:120_000] + "\n\n… truncated …"
			}
			s.setPreview(title, "Path: "+fr.Path+"\nSize: "+fmt.Sprintf("%d", fr.Size)+" bytes\n\n"+body)
		})
	}()
}

func (s *Shell) clearPreviewHint(msg string) {
	s.setPreview("Preview", msg)
}
