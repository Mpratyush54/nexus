package recall

// Tier0 caps keep default injection small (~500-1000 tokens) regardless of
// vault size: retrieval, not concatenation. Tier-1 (memory_search via MCP,
// P4) pulls more on demand. Summarize-on-write (2-3 lines in remember) keeps
// the vault from becoming a second context-window problem.
const (
	MaxEntries = 12
	MaxChars   = 4000
	TopTier0   = 10
)
