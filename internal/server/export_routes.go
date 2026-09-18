// export_routes.go — project memory export/import + fork (implementation-plan-v2 §9).
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"central-memory/internal/store"
)

func (s *Server) registerExportRoutes() {
	s.Mux.HandleFunc("GET /projects/{id}/export", s.requireAuth(s.handleProjectExport))
	s.Mux.HandleFunc("POST /projects/{id}/import", s.requireAuth(s.handleProjectImport))
	s.Mux.HandleFunc("POST /projects/{id}/fork", s.requireAuth(s.handleProjectFork))
}

type exportMemory struct {
	Key            string   `json:"key"`
	Content        string   `json:"content"`
	ContextSnippet string   `json:"context_snippet,omitempty"`
	Level          string   `json:"level,omitempty"`
	Scope          string   `json:"scope,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Status         string   `json:"status,omitempty"`
	Visibility     string   `json:"visibility,omitempty"`
	Confidence     float32  `json:"confidence,omitempty"`
}

type exportDoc struct {
	Version    int            `json:"version"`
	ExportedAt time.Time      `json:"exported_at"`
	Project    *store.Project `json:"project,omitempty"`
	Memories   []exportMemory `json:"memories"`
	Branches   []exportBranch `json:"branches,omitempty"`
}

type exportBranch struct {
	Name       string `json:"name"`
	ParentName string `json:"parent_name,omitempty"`
	Visibility string `json:"visibility,omitempty"`
	OwnerID    string `json:"owner_id,omitempty"`
	ItemCount  int    `json:"item_count,omitempty"`
}

type importRequest struct {
	Memories []exportMemory `json:"memories"`
	Format   string         `json:"format,omitempty"`
	Markdown string         `json:"markdown,omitempty"`
}

type importResult struct {
	ProjectID string   `json:"project_id"`
	Imported  int      `json:"imported"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors,omitempty"`
}

func (s *Server) handleProjectExport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	doc, err := s.buildExportDoc(r.Context(), id, authSubject(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not export: "+err.Error())
		return
	}
	filename := "nexus-export"
	if doc.Project != nil && doc.Project.FolderName != "" {
		filename = "nexus-" + doc.Project.FolderName
	}
	switch format {
	case "json":
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.json"`, filename))
		writeJSON(w, http.StatusOK, doc)
	case "yaml", "yml":
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.yaml"`, filename))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(exportYAML(doc)))
	case "markdown", "md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.md"`, filename))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(exportMarkdown(doc)))
	default:
		writeError(w, http.StatusBadRequest, "format must be json|yaml|markdown")
	}
}

func (s *Server) handleProjectImport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizePermission(w, r, id, store.PermMemoryWrite) {
		return
	}
	var req importRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	items := req.Memories
	if len(items) == 0 && strings.TrimSpace(req.Markdown) != "" {
		items = parseMarkdownMemories(req.Markdown)
	}
	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, "memories (or markdown) is required")
		return
	}
	out := importResult{ProjectID: id}
	subject := authSubject(r)
	for _, em := range items {
		item := store.MemoryItem{
			ProjectID:      id,
			Key:            strings.TrimSpace(em.Key),
			Content:        strings.TrimSpace(em.Content),
			ContextSnippet: em.ContextSnippet,
			Level:          em.Level,
			Scope:          em.Scope,
			Tags:           em.Tags,
			Visibility:     em.Visibility,
			Confidence:     em.Confidence,
			Status:         store.StatusProposed,
			ProposedBy:     subject,
			Source:         "import",
		}
		if item.Key == "" {
			out.Skipped++
			out.Errors = append(out.Errors, "skipped item with empty key")
			continue
		}
		if err := store.ValidateMemoryContent(item.Content); err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, item.Key+": "+err.Error())
			continue
		}
		if len(item.Embedding) != store.EmbeddingDim {
			item.Embedding = s.embedText(r.Context(), item.Key+"\n"+item.Content)
		}
		if err := s.Store.CreateMemoryItem(r.Context(), &item); err != nil {
			out.Skipped++
			out.Errors = append(out.Errors, item.Key+": "+err.Error())
			continue
		}
		out.Imported++
	}
	if len(out.Errors) > 12 {
		out.Errors = out.Errors[:12]
	}
	writeJSON(w, http.StatusOK, out)
}

type forkRequest struct {
	FolderName  string `json:"folder_name"`
	DisplayName string `json:"display_name"`
}

func (s *Server) handleProjectFork(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if !s.authorizeProject(w, r, id) {
		return
	}
	src, err := s.Store.GetProject(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not load project: "+err.Error())
		return
	}
	var req forkRequest
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	folder := strings.TrimSpace(req.FolderName)
	if folder == "" {
		base := src.FolderName
		if base == "" {
			base = "project"
		}
		folder = base + "-fork-" + shortHex(3)
	}
	display := strings.TrimSpace(req.DisplayName)
	if display == "" {
		if src.DisplayName != "" {
			display = src.DisplayName + " (fork)"
		} else {
			display = folder
		}
	}
	subject := authSubject(r)
	dst, err := s.Store.ResolveProject(r.Context(), "", "", folder)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create fork: "+err.Error())
		return
	}
	if dst != nil && dst.ID != "" && time.Since(dst.CreatedAt) > 2*time.Second {
		folder = folder + "-" + shortHex(3)
		dst, err = s.Store.ResolveProject(r.Context(), "", "", folder)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not create fork: "+err.Error())
			return
		}
	}
	_, _ = s.Store.ClaimProject(r.Context(), dst.ID, subject)
	if dst.DisplayName == "" || dst.DisplayName == dst.FolderName {
		dst.DisplayName = display
	}
	copied, skipped, copyErrs := s.copyProjectMemories(r, id, dst.ID, subject)
	branchesCopied := s.copyProjectBranches(r, id, dst.ID, subject)
	writeJSON(w, http.StatusCreated, map[string]any{
		"project":          dst,
		"source_id":        id,
		"memories_copied":  copied,
		"memories_skipped": skipped,
		"branches_copied":  branchesCopied,
		"errors":           copyErrs,
	})
}

func (s *Server) copyProjectMemories(r *http.Request, srcID, dstID, subject string) (copied, skipped int, errs []string) {
	items, err := s.Store.SearchMemory(store.WithViewer(r.Context(), subject), srcID, "", nil, 10000)
	if err != nil {
		return 0, 0, []string{err.Error()}
	}
	ss, ok := s.sharingStore()
	for _, item := range items {
		if item == nil {
			continue
		}
		if ok {
			if _, err := ss.CopyMemory(r.Context(), item.ID, dstID, subject); err != nil {
				skipped++
				errs = append(errs, item.Key+": "+err.Error())
				continue
			}
			copied++
			continue
		}
		dup := *item
		dup.ID = ""
		dup.ProjectID = dstID
		dup.ProposedBy = subject
		dup.ConfirmedBy = ""
		dup.Status = store.StatusProposed
		dup.Embedding = nil
		if err := s.Store.CreateMemoryItem(r.Context(), &dup); err != nil {
			skipped++
			errs = append(errs, item.Key+": "+err.Error())
			continue
		}
		copied++
	}
	if len(errs) > 12 {
		errs = errs[:12]
	}
	return copied, skipped, errs
}

func (s *Server) copyProjectBranches(r *http.Request, srcID, dstID, subject string) int {
	bs, ok := s.branchStore()
	if !ok {
		return 0
	}
	srcBranches, err := bs.ListBranches(r.Context(), srcID)
	if err != nil {
		return 0
	}
	if _, err := bs.EnsureMainBranch(r.Context(), dstID); err != nil {
		return 0
	}
	idMap := map[string]string{} // old id -> new id
	n := 0
	for _, br := range srcBranches {
		if br == nil || br.Name == store.MainBranchName {
			if br != nil {
				if main, err := bs.EnsureMainBranch(r.Context(), dstID); err == nil {
					idMap[br.ID] = main.ID
				}
			}
			continue
		}
		vis := br.Visibility
		if vis == "" {
			vis = store.BranchVisibilityShared
		}
		owner := br.OwnerID
		if owner == "" {
			owner = subject
		}
		parentNew := ""
		if br.ParentBranchID != "" {
			parentNew = idMap[br.ParentBranchID]
		}
		var created *store.MemoryBranch
		var err error
		if parentNew != "" {
			created, err = bs.ForkBranch(r.Context(), parentNew, br.Name, owner, vis, 0)
		} else {
			created, err = bs.CreateBranch(r.Context(), dstID, br.Name, owner, vis)
		}
		if err != nil || created == nil {
			continue
		}
		idMap[br.ID] = created.ID
		if _, err := bs.CopyItemsToBranch(r.Context(), br.ID, created.ID); err == nil {
			n++
		} else {
			n++ // branch row still exists
		}
	}
	return n
}

func (s *Server) buildExportDoc(ctx context.Context, projectID, viewerID string) (*exportDoc, error) {
	p, err := s.Store.GetProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	searchCtx := ctx
	if viewerID != "" {
		searchCtx = store.WithViewer(ctx, viewerID)
	}
	items, err := s.Store.SearchMemory(searchCtx, projectID, "", nil, 10000)
	if err != nil {
		return nil, err
	}
	doc := &exportDoc{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Project:    p,
		Memories:   make([]exportMemory, 0, len(items)),
	}
	for _, it := range items {
		if it == nil {
			continue
		}
		doc.Memories = append(doc.Memories, exportMemory{
			Key:            it.Key,
			Content:        it.Content,
			ContextSnippet: it.ContextSnippet,
			Level:          it.Level,
			Scope:          it.Scope,
			Tags:           it.Tags,
			Status:         it.Status,
			Visibility:     store.NormalizeVisibility(it.Visibility),
			Confidence:     it.Confidence,
		})
	}
	if bs, ok := s.branchStore(); ok {
		branches, err := bs.ListBranches(ctx, projectID)
		if err == nil {
			nameByID := map[string]string{}
			for _, b := range branches {
				if b != nil {
					nameByID[b.ID] = b.Name
				}
			}
			for _, b := range branches {
				if b == nil {
					continue
				}
				eb := exportBranch{Name: b.Name, Visibility: b.Visibility, OwnerID: b.OwnerID}
				if b.ParentBranchID != "" {
					eb.ParentName = nameByID[b.ParentBranchID]
				}
				if own, err := bs.ListBranchItems(ctx, b.ID); err == nil {
					eb.ItemCount = len(own)
				}
				doc.Branches = append(doc.Branches, eb)
			}
		}
	}
	return doc, nil
}

func exportYAML(doc *exportDoc) string {
	raw, _ := json.MarshalIndent(doc, "", "  ")
	return "# nexus export (json-compatible yaml)\n" + string(raw) + "\n"
}

func exportMarkdown(doc *exportDoc) string {
	var b strings.Builder
	name := "project"
	if doc.Project != nil {
		if doc.Project.DisplayName != "" {
			name = doc.Project.DisplayName
		} else if doc.Project.FolderName != "" {
			name = doc.Project.FolderName
		}
	}
	fmt.Fprintf(&b, "# Nexus export — %s\n\n", name)
	fmt.Fprintf(&b, "Exported %s · %d memories\n\n", doc.ExportedAt.UTC().Format(time.RFC3339), len(doc.Memories))
	for _, m := range doc.Memories {
		fmt.Fprintf(&b, "## `%s`\n\n", m.Key)
		fmt.Fprintf(&b, "- level: %s\n- scope: %s\n- status: %s\n- visibility: %s\n",
			m.Level, m.Scope, m.Status, m.Visibility)
		if len(m.Tags) > 0 {
			fmt.Fprintf(&b, "- tags: %s\n", strings.Join(m.Tags, ", "))
		}
		b.WriteString("\n")
		b.WriteString(m.Content)
		b.WriteString("\n\n---\n\n")
	}
	return b.String()
}

func parseMarkdownMemories(md string) []exportMemory {
	var out []exportMemory
	blocks := strings.Split(md, "\n## ")
	for i, block := range blocks {
		if i == 0 && !strings.HasPrefix(strings.TrimSpace(block), "## ") && !strings.HasPrefix(strings.TrimSpace(block), "`") {
			// intro / title
			if strings.Contains(block, "\n## ") {
				continue
			}
		}
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(strings.TrimPrefix(lines[0], "## ")), "`")
		var contentLines []string
		meta := map[string]string{}
		inBody := false
		for _, ln := range lines[1:] {
			trim := strings.TrimSpace(ln)
			if !inBody && strings.HasPrefix(trim, "- ") && strings.Contains(trim, ":") {
				k, v, _ := strings.Cut(strings.TrimPrefix(trim, "- "), ":")
				meta[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
				continue
			}
			if trim == "---" {
				break
			}
			if trim == "" && !inBody {
				inBody = true
				continue
			}
			inBody = true
			contentLines = append(contentLines, ln)
		}
		content := strings.TrimSpace(strings.Join(contentLines, "\n"))
		if key == "" || content == "" {
			continue
		}
		var tags []string
		if raw := meta["tags"]; raw != "" {
			for _, t := range strings.Split(raw, ",") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
		}
		out = append(out, exportMemory{
			Key:        key,
			Content:    content,
			Level:      meta["level"],
			Scope:      meta["scope"],
			Visibility: meta["visibility"],
			Tags:       tags,
		})
	}
	return out
}

func shortHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
