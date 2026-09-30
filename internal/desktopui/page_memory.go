package desktopui

import (
	"fmt"
	"strings"

	"central-memory/internal/cloudclient"

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
	s.memoryTags.OnSubmitted = func(string) {
		go s.runMemorySearch(false)
	}
	onFilter := func(string) { go s.runMemorySearch(false) }
	s.memoryLevel.OnChanged = onFilter
	s.memoryStatus.OnChanged = onFilter
	s.memoryCategory.OnChanged = onFilter

	header := pageHeader("Memory", "Durable facts in Nexus cloud — not files on disk.")
	searchRow := container.NewBorder(nil, nil, nil, searchBtn, s.memorySearch)
	filters := container.NewGridWithColumns(2,
		labeledSelect("Level", s.memoryLevel),
		labeledSelect("Status", s.memoryStatus),
		labeledSelect("Category", s.memoryCategory),
		labeledEntry("Tags", s.memoryTags),
	)

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
			filters,
			s.memoryBanner,
			widget.NewSeparator(),
		),
		nil, nil, nil,
		s.memoryList,
	)
}

func labeledSelect(label string, sel *widget.Select) fyne.CanvasObject {
	return container.NewVBox(dimCaption(label), sel)
}

func labeledEntry(label string, entry *widget.Entry) fyne.CanvasObject {
	return container.NewVBox(dimCaption(label), entry)
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

func (s *Shell) memoryFilterLevel() string {
	if s.memoryLevel == nil {
		return ""
	}
	v := strings.TrimSpace(s.memoryLevel.Selected)
	if v == "" || strings.EqualFold(v, "All levels") {
		return ""
	}
	return v
}

func (s *Shell) memoryFilterStatus() string {
	if s.memoryStatus == nil {
		return ""
	}
	v := strings.TrimSpace(s.memoryStatus.Selected)
	if v == "" || strings.EqualFold(v, "All status") {
		return ""
	}
	return strings.ToUpper(v)
}

func (s *Shell) memoryFilterCategory() string {
	if s.memoryCategory == nil {
		return ""
	}
	v := strings.TrimSpace(s.memoryCategory.Selected)
	if v == "" || strings.EqualFold(v, "All categories") {
		return ""
	}
	return strings.ToLower(v)
}

func (s *Shell) memoryFilterTags() []string {
	if s.memoryTags == nil {
		return nil
	}
	raw := strings.TrimSpace(s.memoryTags.Text)
	if raw == "" {
		return nil
	}
	var tags []string
	for _, p := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(p); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func filterMemoryItems(items []cloudclient.MemoryItem, status, category string) []cloudclient.MemoryItem {
	status = strings.ToUpper(strings.TrimSpace(status))
	category = strings.TrimSpace(strings.ToLower(category))
	if status == "" && category == "" {
		return items
	}
	out := make([]cloudclient.MemoryItem, 0, len(items))
	for _, it := range items {
		st := strings.ToUpper(strings.TrimSpace(it.Status))
		switch status {
		case "PROPOSED":
			if st != "PROPOSED" {
				continue
			}
		case "CONFIRMED":
			// Match web Memory page: confirmed = anything not PROPOSED.
			if st == "PROPOSED" {
				continue
			}
		}
		if category != "" {
			cat := strings.ToLower(strings.TrimSpace(it.Category))
			if cat == "" {
				cat = "general"
			}
			if cat != category {
				continue
			}
		}
		out = append(out, it)
	}
	return out
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
	level := s.memoryFilterLevel()
	tags := s.memoryFilterTags()
	status := s.memoryFilterStatus()
	category := s.memoryFilterCategory()

	items, usedPID, err := s.cloud.MemorySearchFiltered(cloudclient.MemorySearchParams{
		Query:     q,
		ProjectID: pid,
		Limit:     limit,
		Level:     level,
		Tags:      tags,
	})
	if err == nil {
		items = filterMemoryItems(items, status, category)
	}
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
			msg := "No memories match these filters."
			if q != "" {
				msg = "No results for \"" + q + "\" with the current filters."
			}
			s.setMemoryBanner(msg, "Open Home", func() {
				s.switchSection(secHome)
			})
			s.setPreviewKind("memory", "Memory search", msg+pidHint(usedPID))
			return
		}
		s.setMemoryBanner("", "", nil)
		label := fmt.Sprintf("%d memories%s\n\nSelect a row for full details and linked files.", len(items), pidHint(usedPID))
		if q != "" {
			label = fmt.Sprintf("%d results for \"%s\"%s\n\nSelect a row for full details and linked files.", len(items), q, pidHint(usedPID))
		}
		s.setPreviewKind("memory", "Memory", label)
	})
}
