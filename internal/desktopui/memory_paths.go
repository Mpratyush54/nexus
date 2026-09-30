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
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return path
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(filepath.Clean(path))
	}
	abs := filepath.Clean(path)
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return path
	}
	if strings.HasPrefix(rel, "..") {
		return path
	}
	return filepath.ToSlash(rel)
}
