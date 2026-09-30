package desktopui

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"central-memory/internal/cloudclient"
)

var (
	reBacktickPath = regexp.MustCompile("`([^`\n]{2,260})`")
	reMarkdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]{2,260})\)`)
	// Absolute Windows/Unix, or relative with a slash + file-ish suffix.
	reLoosePath = regexp.MustCompile(`(?i)(?:[a-z]:[\\/][^\s"'<>|*?]{1,240}|/(?:Users|home|var|tmp|opt|usr|etc|mnt|Volumes)[^\s"'<>|*?]{1,240}|(?:\.{0,2}/)?[\w.-]+(?:/[\w.-]+){1,12}\.(?:go|ts|tsx|js|jsx|py|rs|md|json|yml|yaml|toml|css|html|vue|svelte|java|kt|swift|c|cpp|h|hpp|sh|ps1|sql|proto|txt))`)
)

// extractFilePaths finds path-looking strings in memory content plus API files_affected.
func extractFilePaths(content string, filesAffected []string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		p = strings.TrimSuffix(p, ",")
		p = strings.TrimSuffix(p, ".")
		p = strings.TrimSuffix(p, ";")
		p = strings.TrimSpace(p)
		if !looksLikeFilePath(p) {
			return
		}
		key := strings.ToLower(filepath.ToSlash(p))
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	for _, p := range filesAffected {
		add(p)
	}
	for _, m := range reBacktickPath.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range reMarkdownLink.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range reLoosePath.FindAllString(content, -1) {
		add(m)
	}
	return out
}

func looksLikeFilePath(p string) bool {
	if p == "" || len(p) < 3 || len(p) > 260 {
		return false
	}
	if strings.ContainsAny(p, "<>|*?\n\r\t") {
		return false
	}
	lower := strings.ToLower(p)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "mailto:") {
		return false
	}
	if !strings.ContainsAny(p, `/\`) && !strings.Contains(p, ":") {
		return false
	}
	base := filepath.Base(p)
	if base == "" || base == "." || base == ".." {
		return false
	}
	if strings.Contains(base, ".") {
		ext := filepath.Ext(base)
		if len(ext) >= 2 && len(ext) <= 8 {
			ok := true
			for _, r := range ext[1:] {
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
		return true
	}
	return strings.ContainsAny(p, `/\`)
}

// memoryFileRefs returns display paths for a memory hit (API fields + content parse).
func memoryFileRefs(it cloudclient.MemoryItem) []string {
	return extractFilePaths(it.Content, it.FilesAffected)
}

// formatMemoryDetail builds the textual detail block (metadata + content).
func formatMemoryDetail(it cloudclient.MemoryItem) string {
	var b strings.Builder
	writeKV := func(k, v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		b.WriteString(k + ": " + v + "\n")
	}
	writeKV("Key", it.Key)
	writeKV("ID", it.ID)
	writeKV("Project", it.ProjectID)
	if it.Level != "" || it.Scope != "" {
		b.WriteString("Level: " + orDash(it.Level) + " · Scope: " + orDash(it.Scope) + "\n")
	}
	writeKV("Status", it.Status)
	writeKV("Source", it.Source)
	writeKV("Category", it.Category)
	if it.Confidence > 0 {
		b.WriteString("Confidence: " + strconv.FormatFloat(it.Confidence, 'f', 3, 64) + "\n")
	}
	if len(it.Tags) > 0 {
		writeKV("Tags", strings.Join(it.Tags, ", "))
	}
	writeKV("Created", it.CreatedAt)
	writeKV("Updated", it.UpdatedAt)
	writeKV("Last used", it.LastUsedAt)
	if snip := strings.TrimSpace(it.ContextSnippet); snip != "" {
		b.WriteString("\nContext:\n" + snip + "\n")
	}
	b.WriteString("\n--- Content ---\n\n")
	b.WriteString(strings.TrimSpace(it.Content))
	return b.String()
}

// resolveReadPath maps an extracted path to a daemon ReadFile argument.
// Prefers workspace-relative when the path sits under root.
func resolveReadPath(path, workspaceRoot string) string {
	cands := readPathCandidates(path, workspaceRoot)
	if len(cands) == 0 {
		return strings.TrimSpace(path)
	}
	return cands[0]
}

// readPathCandidates returns daemon ReadFile path attempts for a memory file link.
// Tries workspace-relative forms first (SecureJoin), including stripping a leading
// project folder segment that memories sometimes include (e.g. central-memory/internal/…).
func readPathCandidates(path, workspaceRoot string) []string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, `"'`)
	if path == "" {
		return nil
	}
	root := strings.TrimSpace(workspaceRoot)
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		// Daemon SecureJoin accepts either slash style; prefer ToSlash for stability.
		p = filepath.ToSlash(filepath.Clean(p))
		if filepath.IsAbs(p) {
			// Keep OS abs for allow check; Rel below may replace it.
		}
		key := strings.ToLower(p)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}

	if root != "" {
		rootClean := filepath.Clean(root)
		base := filepath.Base(rootClean)

		if filepath.IsAbs(path) {
			abs := filepath.Clean(path)
			if rel, err := filepath.Rel(rootClean, abs); err == nil && !strings.HasPrefix(rel, "..") {
				add(rel)
			}
			add(abs) // last resort; daemon will 403 if outside
		} else {
			add(path)
			slash := filepath.ToSlash(path)
			add(slash)
			// Strip leading "./"
			add(strings.TrimPrefix(slash, "./"))
			// Strip project folder prefix: "central-memory/internal/…" → "internal/…"
			if base != "" && base != "." && base != string(filepath.Separator) {
				prefix := filepath.ToSlash(base) + "/"
				if strings.HasPrefix(strings.ToLower(slash), strings.ToLower(prefix)) {
					add(slash[len(prefix):])
				}
				// Also "central-memory\" on Windows-style stored paths
				prefixWin := base + `\`
				if strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefixWin)) {
					add(path[len(prefixWin):])
				}
			}
		}
	} else {
		add(path)
		add(filepath.ToSlash(path))
	}
	return out
}
