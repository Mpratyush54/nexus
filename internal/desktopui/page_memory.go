package desktopui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func (s *Shell) memoryPage() fyne.CanvasObject {
	searchBtn := secondaryButton("Search", func() {
		go s.runMemorySearch(false)
	})
	searchBtn.Icon = theme.SearchIcon()
	s.memorySearch.OnSubmitted = func(string) {
		go s.runMemorySearch(false)
	}

	header := pageHeader("Memory", "Durable facts in Nexus cloud — not files on disk.")
	searchRow := container.NewBorder(nil, nil, nil, searchBtn, s.memorySearch)

	// Auto-browse recent on open when the list is empty.
	s.mu.Lock()
	needLoad := len(s.memoryItems) == 0 && s.signedIn
	s.mu.Unlock()
	if needLoad {
		go s.runMemorySearch(true)
	}

	return container.NewBorder(
		container.NewVBox(
			header,
			searchRow,
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
		objs = append(objs, outlineButton(cta, onCTA))
	}
	s.memoryBanner.Objects = []fyne.CanvasObject{container.NewVBox(objs...)}
	s.memoryBanner.Refresh()
}

func (s *Shell) runMemorySearch(browseIfEmpty bool) {
	q := strings.TrimSpace(s.memorySearch.Text)
	if q == "" && !browseIfEmpty {
		// Explicit Search with empty box → browse recent.
		browseIfEmpty = true
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

	if pid == "" {
		projects, err := s.cloud.ListProjects()
		if err == nil && len(projects) == 1 {
			pid = projects[0].ID
		} else if err == nil && len(projects) > 1 {
			fyne.Do(func() {
				names := make([]string, 0, len(projects))
				for _, p := range projects {
					names = append(names, first(p.DisplayName, p.FolderName, p.ID))
				}
				s.setMemoryBanner("Multiple projects on this account. Link a workspace on Home so harvest sets the active project, or search after connecting.", "Open Home", func() {
					s.switchSection(secHome)
				})
				s.setPreviewKind("memory", "Memory", "Projects:\n- "+strings.Join(names, "\n- "))
			})
			return
		} else if err != nil {
			fyne.Do(func() {
				s.setMemoryBanner("Could not resolve project: "+err.Error(), "Open Home", func() {
					s.switchSection(secHome)
				})
			})
			return
		}
	}

	if pid == "" {
		fyne.Do(func() {
			s.setMemoryBanner("No linked project yet. Finish setup on Home (workspace + harvest) so memory can load.", "Open Home", func() {
				s.switchSection(secHome)
			})
			s.setPreviewKind("memory", "Memory", "Missing project_id — choose a workspace and wait for harvest to link a project.")
		})
		return
	}

	limit := 25
	if q == "" {
		limit = 40
	}
	items, usedPID, err := s.cloud.MemorySearch(q, pid, limit)
	fyne.Do(func() {
		if err != nil {
			s.setMemoryBanner("Search failed: "+err.Error(), "Open Home", func() {
				s.switchSection(secHome)
			})
			s.setPreviewKind("memory", "Memory search", err.Error())
			return
		}
		s.mu.Lock()
		s.memoryItems = items
		s.mu.Unlock()
		s.memoryList.Refresh()
		if len(items) == 0 {
			msg := "No memories yet for this project."
			if q != "" {
				msg = "No results for \"" + q + "\". Try another query or confirm the linked project on Home."
			}
			s.setMemoryBanner(msg, "Open Home", func() {
				s.switchSection(secHome)
			})
			s.setPreviewKind("memory", "Memory search", msg+pidHint(usedPID))
			return
		}
		s.setMemoryBanner("", "", nil)
		label := fmt.Sprintf("%d recent memories%s\n\nSelect a row for full details and linked files.", len(items), pidHint(usedPID))
		if q != "" {
			label = fmt.Sprintf("%d results for \"%s\"%s\n\nSelect a row for full details and linked files.", len(items), q, pidHint(usedPID))
		}
		s.setPreviewKind("memory", "Memory", label)
	})
}
