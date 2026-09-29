package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SnapshotLayout is the on-disk mapping for one harness conversation.
// TranscriptFile is the primary dialogue file; ArtifactDir is the "brain"
// (custom agent state) packed into ArtifactsBundle for cross-device restore.
// SQLiteSource is set when a .db/.vscdb holds dialogue and no JSONL sidecar
// exists yet — CollectSnapshot extracts turns into TranscriptPayload at
// collect time (no permanent sidecar required; ADR-033 CLI/stdlib path).
type SnapshotLayout struct {
	Harness        string
	ConversationID string
	TranscriptFile string
	ArtifactDir    string
	SQLiteSource   string // optional; empty when JSONL is authoritative
}

// SnapshotSupportedHarnesses lists harvest agents with snapshot/restore adapters.
func SnapshotSupportedHarnesses() []string {
	return []string{
		"antigravity", "cursor", "claude", "codex", "copilot", "windsurf",
		"gemini", "opencode", "grok", "kimi", "codeium", "commandcode",
		"cagent", "zcode", "deepseek", "hermes",
	}
}

// HarnessSupported reports whether harness has a snapshot/restore adapter.
func HarnessSupported(harness string) bool {
	h := normalizeHarness(harness)
	if h == "" {
		return false
	}
	for _, name := range SnapshotSupportedHarnesses() {
		if name == h {
			return true
		}
	}
	return false
}

func normalizeHarness(harness string) string {
	h := strings.ToLower(strings.TrimSpace(harness))
	// Historical alias: early snapshot code treated gemini as antigravity.
	// Gemini CLI is separate; only empty defaults to antigravity.
	if h == "" {
		return "antigravity"
	}
	return h
}

// ResolveSnapshotLayout returns collect/restore paths for a harness session.
// workspaceRoot helps pick the correct Cursor/Claude project slug when the
// conversation id alone is ambiguous.
func ResolveSnapshotLayout(harness, conversationID, workspaceRoot string) (SnapshotLayout, error) {
	harness = normalizeHarness(harness)
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return SnapshotLayout{}, fmt.Errorf("snapshot: empty conversation id")
	}
	if !HarnessSupported(harness) {
		return SnapshotLayout{}, fmt.Errorf("snapshot: harness %q not supported", harness)
	}
	home, _ := os.UserHomeDir()
	layout := SnapshotLayout{Harness: harness, ConversationID: conversationID}

	switch harness {
	case "antigravity":
		brain := filepath.Join(home, ".gemini", "antigravity", "brain", conversationID)
		layout.ArtifactDir = brain
		layout.TranscriptFile = antigravityTranscriptPath(brain)
		return layout, nil

	case "cursor":
		return resolveCursorLayout(home, conversationID, workspaceRoot)

	case "claude":
		return resolveClaudeLayout(home, conversationID, workspaceRoot)

	case "codex":
		codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		return resolveSearchLayout(harness, conversationID, []string{
			filepath.Join(codexHome, "sessions"),
			filepath.Join(codexHome, "archived_sessions"),
		}, filepath.Join(codexHome, "sessions", "nexus-restored", conversationID))

	case "copilot":
		return resolveSearchLayout(harness, conversationID, []string{
			filepath.Join(home, ".copilot", "session-state"),
			filepath.Join(home, ".copilot"),
		}, filepath.Join(home, ".copilot", "session-state", conversationID+".jsonl"))

	case "windsurf":
		appData := os.Getenv("APPDATA")
		localApp := os.Getenv("LOCALAPPDATA")
		roots := []string{filepath.Join(home, ".windsurf")}
		if appData != "" {
			roots = append(roots, filepath.Join(appData, "Windsurf", "User", "workspaceStorage"))
		}
		if localApp != "" {
			roots = append(roots, filepath.Join(localApp, "Windsurf", "User", "workspaceStorage"))
		}
		// Windsurf is primarily SQLite; JSONL is synthesized at collect time
		// from state.vscdb when no sidecar exists (see snapshot_sqlite.go).
		return resolveSearchLayout(harness, conversationID, roots,
			filepath.Join(home, ".windsurf", "nexus-restored", conversationID+".jsonl"))

	case "gemini":
		// Gemini CLI chats: ~/.gemini/tmp/<hash>/chats/*.jsonl — not antigravity brains.
		return resolveSearchLayout(harness, conversationID, []string{
			filepath.Join(home, ".gemini", "tmp"),
			filepath.Join(home, ".gemini"),
		}, filepath.Join(home, ".gemini", "tmp", "nexus-restored", "chats", conversationID+".jsonl"))

	case "opencode":
		xdgData := os.Getenv("XDG_DATA_HOME")
		if xdgData == "" {
			xdgData = filepath.Join(home, ".local", "share")
		}
		xdgConfig := os.Getenv("XDG_CONFIG_HOME")
		if xdgConfig == "" {
			xdgConfig = filepath.Join(home, ".config")
		}
		return resolveSearchLayout(harness, conversationID, []string{
			filepath.Join(xdgConfig, "opencode"),
			filepath.Join(home, ".config", "opencode"),
			filepath.Join(xdgData, "opencode"),
			filepath.Join(home, ".local", "share", "opencode"),
			filepath.Join(home, ".opencode"),
		}, filepath.Join(home, ".opencode", "nexus-restored", conversationID+".jsonl"))

	case "grok":
		return resolveSearchLayout(harness, conversationID, []string{filepath.Join(home, ".grok")},
			filepath.Join(home, ".grok", "nexus-restored", conversationID+".jsonl"))
	case "kimi":
		return resolveSearchLayout(harness, conversationID, []string{
			filepath.Join(home, ".kimi-code"), filepath.Join(home, ".kimi"),
		}, filepath.Join(home, ".kimi-code", "nexus-restored", conversationID+".jsonl"))
	case "codeium":
		return resolveSearchLayout(harness, conversationID, []string{filepath.Join(home, ".codeium")},
			filepath.Join(home, ".codeium", "nexus-restored", conversationID+".jsonl"))
	case "commandcode":
		return resolveSearchLayout(harness, conversationID, []string{filepath.Join(home, ".commandcode")},
			filepath.Join(home, ".commandcode", "nexus-restored", conversationID+".jsonl"))
	case "cagent":
		return resolveSearchLayout(harness, conversationID, []string{filepath.Join(home, ".cagent")},
			filepath.Join(home, ".cagent", "nexus-restored", conversationID+".jsonl"))
	case "zcode":
		return resolveSearchLayout(harness, conversationID, []string{
			filepath.Join(home, ".zcode", "cli", "agents"),
			filepath.Join(home, ".zcode", "cli", "rollout"),
			filepath.Join(home, ".zcode", "v2", "sessions"),
			filepath.Join(home, ".zcode"),
		}, filepath.Join(home, ".zcode", "nexus-restored", conversationID+".jsonl"))
	case "deepseek":
		xdgData := os.Getenv("XDG_DATA_HOME")
		if xdgData == "" {
			xdgData = filepath.Join(home, ".local", "share")
		}
		return resolveSearchLayout(harness, conversationID, []string{
			filepath.Join(xdgData, "deepseek-cli"),
			filepath.Join(home, ".local", "share", "deepseek-cli"),
			filepath.Join(home, ".deepseek-cli"),
			filepath.Join(home, ".deepseek"),
		}, filepath.Join(home, ".deepseek-cli", "nexus-restored", conversationID+".jsonl"))
	case "hermes":
		return resolveSearchLayout(harness, conversationID, []string{
			filepath.Join(home, ".hermes", "sessions"),
			filepath.Join(home, ".hermes"),
		}, filepath.Join(home, ".hermes", "sessions", "nexus-restored", conversationID+".jsonl"))
	}
	return SnapshotLayout{}, fmt.Errorf("snapshot: harness %q not supported", harness)
}

// SnapshotHarnessPaths returns transcript directory and artifact ("brain") root.
// Prefer ResolveSnapshotLayout when the exact transcript file path is needed.
func SnapshotHarnessPaths(harness, conversationID string) (transcriptDir, brainDir string) {
	layout, err := ResolveSnapshotLayout(harness, conversationID, "")
	if err != nil {
		return "", ""
	}
	if layout.TranscriptFile != "" {
		transcriptDir = filepath.Dir(layout.TranscriptFile)
	}
	return transcriptDir, layout.ArtifactDir
}

// DetectHarnessForConversation picks the harness whose transcript already exists
// for conversationID (used when runtime has no agent hint). SQLite-only stores
// count when a matching .db/.vscdb is present (extract-at-collect).
func DetectHarnessForConversation(conversationID, workspaceRoot string) string {
	for _, h := range SnapshotSupportedHarnesses() {
		layout, err := ResolveSnapshotLayout(h, conversationID, workspaceRoot)
		if err != nil {
			continue
		}
		if layout.TranscriptFile != "" {
			if st, err := os.Stat(layout.TranscriptFile); err == nil && !st.IsDir() {
				return h
			}
		}
		if layout.SQLiteSource != "" {
			if st, err := os.Stat(layout.SQLiteSource); err == nil && !st.IsDir() {
				return h
			}
		}
	}
	return ""
}

func antigravityTranscriptPath(brainDir string) string {
	full := filepath.Join(brainDir, ".system_generated", "logs", "transcript_full.jsonl")
	if st, err := os.Stat(full); err == nil && !st.IsDir() {
		return full
	}
	return filepath.Join(brainDir, ".system_generated", "logs", "transcript.jsonl")
}

func resolveCursorLayout(home, conversationID, workspaceRoot string) (SnapshotLayout, error) {
	projectsRoot := filepath.Join(home, ".cursor", "projects")
	slug := cursorWorkspaceSlug(workspaceRoot)
	candidates := []string{}
	if slug != "" {
		candidates = append(candidates, filepath.Join(projectsRoot, slug))
	}
	// Fall back: any project whose agent-transcripts contains the session file.
	if entries, err := os.ReadDir(projectsRoot); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(projectsRoot, e.Name())
			if slug != "" && strings.EqualFold(e.Name(), slug) {
				continue
			}
			candidates = append(candidates, p)
		}
	}
	var foundProj string
	var foundTr string
	for _, proj := range candidates {
		tr := filepath.Join(proj, "agent-transcripts", conversationID+".jsonl")
		if st, err := os.Stat(tr); err == nil && !st.IsDir() {
			foundProj, foundTr = proj, tr
			break
		}
		// Cursor sometimes nests uuid/uuid.jsonl
		nested := filepath.Join(proj, "agent-transcripts", conversationID, conversationID+".jsonl")
		if st, err := os.Stat(nested); err == nil && !st.IsDir() {
			foundProj, foundTr = proj, nested
			break
		}
	}
	if foundProj == "" {
		// Restore target: preferred slug, else nexus-restored bucket.
		if slug == "" {
			slug = "nexus-restored"
		}
		foundProj = filepath.Join(projectsRoot, slug)
		foundTr = filepath.Join(foundProj, "agent-transcripts", conversationID+".jsonl")
	}
	return SnapshotLayout{
		Harness:        "cursor",
		ConversationID: conversationID,
		TranscriptFile: foundTr,
		// Project dir holds agent-tools / canvases / assets (the Cursor "brain").
		ArtifactDir: foundProj,
	}, nil
}

func resolveClaudeLayout(home, conversationID, workspaceRoot string) (SnapshotLayout, error) {
	projectsRoot := filepath.Join(home, ".claude", "projects")
	encoded := claudeProjectEncoded(workspaceRoot)
	candidates := []string{}
	if encoded != "" {
		candidates = append(candidates, filepath.Join(projectsRoot, encoded))
		// Claude may vary drive-letter case on Windows.
		candidates = append(candidates, filepath.Join(projectsRoot, strings.ToLower(encoded)))
		candidates = append(candidates, filepath.Join(projectsRoot, strings.ToUpper(encoded[:1])+encoded[1:]))
	}
	if entries, err := os.ReadDir(projectsRoot); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(projectsRoot, e.Name())
			dup := false
			for _, c := range candidates {
				if strings.EqualFold(c, p) {
					dup = true
					break
				}
			}
			if !dup {
				candidates = append(candidates, p)
			}
		}
	}
	for _, proj := range candidates {
		tr := filepath.Join(proj, conversationID+".jsonl")
		if st, err := os.Stat(tr); err == nil && !st.IsDir() {
			return SnapshotLayout{
				Harness: "claude", ConversationID: conversationID,
				TranscriptFile: tr, ArtifactDir: proj,
			}, nil
		}
	}
	targetProj := filepath.Join(projectsRoot, "nexus-restored")
	if encoded != "" {
		targetProj = filepath.Join(projectsRoot, encoded)
	}
	return SnapshotLayout{
		Harness: "claude", ConversationID: conversationID,
		TranscriptFile: filepath.Join(targetProj, conversationID+".jsonl"),
		ArtifactDir:    targetProj,
	}, nil
}

// resolveSearchLayout finds conversationID.jsonl under roots; if missing,
// looks for a matching SQLite store; restoreTarget is the write path for
// restore when synthesizing JSONL from SQLite (parent becomes ArtifactDir
// unless a live SQLite dir is a better brain root).
func resolveSearchLayout(harness, conversationID string, roots []string, restoreTarget string) (SnapshotLayout, error) {
	wantNames := []string{
		conversationID + ".jsonl",
		conversationID + ".json",
		"transcript.jsonl",
		"transcript_full.jsonl",
	}
	var jsonlHit, sqliteHit string
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if jsonlHit == "" {
			jsonlHit = findNamedFile(root, conversationID, wantNames, 6)
		}
		if sqliteHit == "" {
			sqliteHit = findSQLiteFile(root, conversationID, harness, 6)
		}
		if jsonlHit != "" {
			break
		}
	}
	if jsonlHit != "" {
		return SnapshotLayout{
			Harness: harness, ConversationID: conversationID,
			TranscriptFile: jsonlHit,
			ArtifactDir:    filepath.Dir(jsonlHit),
			SQLiteSource:   sqliteHit, // optional companion DB (may still pack if small)
		}, nil
	}
	if sqliteHit != "" {
		tr := restoreTarget
		if tr == "" {
			tr = filepath.Join(filepath.Dir(sqliteHit), conversationID+".jsonl")
		}
		return SnapshotLayout{
			Harness: harness, ConversationID: conversationID,
			TranscriptFile: tr,
			ArtifactDir:    filepath.Dir(sqliteHit),
			SQLiteSource:   sqliteHit,
		}, nil
	}
	if restoreTarget == "" {
		return SnapshotLayout{}, fmt.Errorf("snapshot: no path for harness %s session %s", harness, conversationID)
	}
	return SnapshotLayout{
		Harness: harness, ConversationID: conversationID,
		TranscriptFile: restoreTarget,
		ArtifactDir:    filepath.Dir(restoreTarget),
	}, nil
}

// findSQLiteFile locates a .db/.vscdb/.sqlite whose stem matches conversationID
// (harvest sessionID is basename-without-ext), or known global names for the
// harness (opencode.db). Prefers exact stem matches over globals.
func findSQLiteFile(root, conversationID, harness string, maxDepth int) string {
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return ""
	}
	convLower := strings.ToLower(strings.TrimSpace(conversationID))
	wantExt := map[string]bool{".db": true, ".vscdb": true, ".sqlite": true, ".sqlite3": true}
	var exact, global string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || (exact != "" && global != "") {
			return nil
		}
		if info.IsDir() {
			rel, _ := filepath.Rel(root, path)
			if rel != "." && strings.Count(rel, string(os.PathSeparator)) >= maxDepth {
				return filepath.SkipDir
			}
			base := strings.ToLower(info.Name())
			if harvesterSkipDirs[base] || base == "node_modules" || base == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(info.Name()))
		if !wantExt[ext] {
			return nil
		}
		base := info.Name()
		stem := strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
		if stem == convLower {
			exact = path
			return filepath.SkipAll
		}
		// OpenCode: single global DB; harvest session id is "opencode".
		if harness == "opencode" && strings.EqualFold(base, "opencode.db") {
			if convLower == "opencode" || convLower == "" {
				global = path
			}
		}
		return nil
	})
	if exact != "" {
		return exact
	}
	return global
}

func findNamedFile(root, conversationID string, wantNames []string, maxDepth int) string {
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return ""
	}
	var found string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if info.IsDir() {
			rel, _ := filepath.Rel(root, path)
			if rel != "." && strings.Count(rel, string(os.PathSeparator)) >= maxDepth {
				return filepath.SkipDir
			}
			base := strings.ToLower(info.Name())
			if harvesterSkipDirs[base] || base == "node_modules" || base == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		base := info.Name()
		for _, want := range wantNames {
			if strings.EqualFold(base, want) {
				// Prefer exact conversation id match over generic transcript.jsonl
				// unless the parent dir is the conversation id.
				if strings.HasPrefix(strings.ToLower(want), "transcript") {
					parent := filepath.Base(filepath.Dir(path))
					if !strings.EqualFold(parent, conversationID) &&
						!strings.Contains(strings.ToLower(path), strings.ToLower(conversationID)) {
						continue
					}
				}
				found = path
				return filepath.SkipAll
			}
		}
		// Basename without ext equals conversation id.
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if strings.EqualFold(stem, conversationID) {
			ext := strings.ToLower(filepath.Ext(base))
			if ext == ".jsonl" || ext == ".json" {
				found = path
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

// cursorWorkspaceSlug mirrors Cursor's projects/<slug> naming:
// D:\central-memory → d-central-memory.
func cursorWorkspaceSlug(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = filepath.Clean(root)
	} else {
		abs = filepath.Clean(abs)
	}
	vol := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, vol)
	rest = strings.Trim(rest, `/\`)
	var parts []string
	if vol != "" {
		parts = append(parts, strings.ToLower(strings.TrimSuffix(vol, ":")))
	}
	for _, p := range strings.FieldsFunc(rest, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if p == "" || p == "." {
			continue
		}
		parts = append(parts, strings.ToLower(p))
	}
	return strings.Join(parts, "-")
}

// claudeProjectEncoded mirrors Claude Code's ~/.claude/projects/<encoded>/ dirs:
// D:\central-memory → D--central-memory.
func claudeProjectEncoded(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = filepath.Clean(root)
	} else {
		abs = filepath.Clean(abs)
	}
	s := abs
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, `\`, "-")
	s = strings.ReplaceAll(s, `/`, "-")
	return s
}
