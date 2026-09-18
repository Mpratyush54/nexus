package project

import (
	"fmt"
	"io"
	"strings"
)

// Shared CLI helpers (Issue #84): the root `mem` binary (./main.go) and the
// `cmd/mem` entrypoint implement the same `projects`/`status` surface. Both
// must call these helpers so formatting and identity stay unified. The
// `nexus` CLI (cmd/nexus) is a server client with its own `status` meaning
// (server health); its usage text cross-references these local commands.

// FormatLeafLine renders one `projects` line: the leaf ID plus move-proof
// identity (git origin URL, else short root-commit hash). LeafDir resolves
// the leaf to its absolute directory before fingerprinting.
func FormatLeafLine(leaf string) string {
	origin, root := Fingerprint(LeafDir(leaf))
	id := leaf
	if origin != "" {
		id += "  [" + origin + "]"
	} else if root != "" && len(root) >= 12 {
		id += "  [root " + root[:12] + "]"
	}
	return id
}

// ProjectLines returns the formatted `projects` lines for all leaves.
func ProjectLines() []string {
	leaves := Leaves()
	out := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		out = append(out, FormatLeafLine(leaf))
	}
	return out
}

// PrintProjects writes the unified `projects` listing to w.
func PrintProjects(w io.Writer) error {
	for _, line := range ProjectLines() {
		fmt.Fprintln(w, line)
	}
	return nil
}

// StatusSummary returns the shared local health lines used by both `mem`
// entrypoints: projects detected plus daemon/server placeholders. The nexus
// CLI reports server health instead; see cmd/nexus `status`.
func StatusSummary() []string {
	leaves := CachedLeaves()
	return []string{
		"mem status — multiplayer central memory",
		"",
		fmt.Sprintf("projects detected: %d", len(leaves)),
		"daemon: not yet implemented",
		"server: not yet implemented",
	}
}

// PrintStatus writes the unified `status` report to w.
func PrintStatus(w io.Writer) error {
	for _, line := range StatusSummary() {
		fmt.Fprintln(w, strings.TrimSuffix(line, "\n"))
	}
	return nil
}
