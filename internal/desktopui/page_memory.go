package desktopui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) memoryPage() fyne.CanvasObject {
	searchBtn := widget.NewButton("Search", func() {
		go s.runMemorySearch()
	})
	s.memorySearch.OnSubmitted = func(string) {
		go s.runMemorySearch()
	}
	hint := mutedLabel("Memory entries are durable facts in Nexus cloud — not files on disk.")
	return container.NewBorder(
		container.NewVBox(
			sectionHeading("Memory"),
			hint,
			container.NewBorder(nil, nil, nil, searchBtn, s.memorySearch),
			s.memoryBanner,
			widget.NewSeparator(),
		),
		nil, nil, nil,
		s.memoryList,
	)
}

func (s *Shell) setMemoryBanner(msg string, cta string, onCTA func()) {
	if strings.TrimSpace(msg) == "" {
		s.memoryBanner.Objects = nil
		s.memoryBanner.Refresh()
		return
	}
	objs := []fyne.CanvasObject{mutedLabel(msg)}
	if cta != "" && onCTA != nil {
		objs = append(objs, widget.NewButton(cta, onCTA))
	}
	s.memoryBanner.Objects = []fyne.CanvasObject{container.NewVBox(objs...)}
	s.memoryBanner.Refresh()
}

func (s *Shell) runMemorySearch() {
	q := strings.TrimSpace(s.memorySearch.Text)
	if q == "" {
		fyne.Do(func() {
			s.setMemoryBanner("Enter a search query to find cloud memory.", "", nil)
		})
		return
	}

	s.mu.Lock()
	signedIn := s.signedIn
	pid := memoryProjectID(s.cachedStatus, s.cachedHarvest)
	s.mu.Unlock()

	if !signedIn {
		fyne.Do(func() {
			s.setMemoryBanner("Sign in to search project memory.", "Sign in", func() {
				if s.hooks.OnSignIn != nil {
					s.hooks.OnSignIn()
				}
			})
			s.setPreviewKind("memory", "Memory", "Not signed in.")
		})
		return
	}

	items, usedPID, err := s.cloud.MemorySearch(q, pid, 25)
	fyne.Do(func() {
		if err != nil {
			s.setMemoryBanner("Search failed: "+err.Error(), "Open Connect", func() {
				s.switchSection(secConnect)
			})
			s.setPreviewKind("memory", "Memory search", err.Error())
			return
		}
		s.mu.Lock()
		s.memoryItems = items
		s.mu.Unlock()
		s.memoryList.Refresh()
		if len(items) == 0 {
			s.setMemoryBanner("No results for \""+q+"\". Try another query or confirm the linked project on Connect.", "Open Connect", func() {
				s.switchSection(secConnect)
			})
			s.setPreviewKind("memory", "Memory search", "No results for \""+q+"\"."+pidHint(usedPID))
			return
		}
		s.setMemoryBanner("", "", nil)
		s.setPreviewKind("memory", "Memory search", fmt.Sprintf("%d results for \"%s\"%s\n\nSelect a row to preview.", len(items), q, pidHint(usedPID)))
	})
}
