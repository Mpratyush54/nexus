package store

// memory_constants.go — additive Level/Status constants for the #35 store
// seam (memory_transitions.go).
//
// Master (db.go/models.go/memory.go) uses raw strings ("PROPOSED",
// "project", ...) with no exported constants. Defining them here is purely
// additive: no master file is touched, values transcribe the
// migrations/001 CHECK constraints exactly (plus 'ephemeral' from 009).
const (
	StatusProposed   = "PROPOSED"
	StatusConfirmed  = "CONFIRMED"
	StatusRejected   = "REJECTED"
	StatusSuperseded = "SUPERSEDED"
)

const (
	LevelOrganization = "organization"
	LevelProject      = "project"
	LevelPersonal     = "personal"
	LevelSession      = "session"
	// LevelEphemeral lives in validation.go (canonical 5th-tier const);
	// it wins every override contest (see context.LevelRank) and is never
	// emitted by heuristic classification — producers assign it explicitly.
)

// FormatEmbedding renders a vector for an embedding parameter. It wraps
// encodeEmbedding (db.go): empty means "no embedding yet" -> NULL so the
// row stays text-searchable. Defined here so the #35 seam
// (memory_transitions.go) compiles without touching master's db.go.
func FormatEmbedding(vec []float32) any {
	return encodeEmbedding(vec)
}
