package memoryact

import (
	"fmt"
	"strings"
)

// Public status vocabulary (product spec 2.2). Legacy rows stay stored as
// PROPOSED / CONFIRMED / REJECTED / SUPERSEDED; JSON uses this helper.
const (
	PublicActive     = "active"
	PublicSuperseded = "superseded"
	PublicForgotten  = "forgotten"
)

// PrivateSessionSummary is the teammate-facing provenance line. It names no
// session and carries no title.
const PrivateSessionSummary = "from a private session"

// PublicStatus maps a stored memory status onto active, superseded, or
// forgotten. PROPOSED and CONFIRMED are active. REJECTED is forgotten
// because migrations/001 rejects the literal "forgotten" and forget writes
// REJECTED on Postgres. SUPERSEDED is superseded.
func PublicStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "", "PROPOSED", "CONFIRMED", "ACTIVE":
		return PublicActive
	case "SUPERSEDED":
		return PublicSuperseded
	case "FORGOTTEN", "REJECTED":
		return PublicForgotten
	default:
		return PublicActive
	}
}

// CanonicalLevel accepts session, project, personal, or organization.
// Team is the product name for organization (spec 2.1). Unknown values,
// including ephemeral, are rejected.
func CanonicalLevel(level string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "session":
		return "session", true
	case "project":
		return "project", true
	case "personal":
		return "personal", true
	case "organization", "team":
		return "organization", true
	default:
		return "", false
	}
}

// Provenance is recorded when a session fact becomes project knowledge.
// It never carries a session title.
type Provenance struct {
	SourceSessionID     string
	SourceOwnerID       string
	PromotedFromPrivate bool
	RemovedByOwner      bool
}

// ProvenanceView is the caller-specific shape of Provenance.
type ProvenanceView struct {
	SourceSessionID     string `json:"source_session_id,omitempty"`
	SourceOwnerID       string `json:"source_owner_id,omitempty"`
	PromotedFromPrivate bool   `json:"promoted_from_private,omitempty"`
	Summary             string `json:"summary,omitempty"`
	RemovedByOwner      bool   `json:"removed_by_owner,omitempty"`
}

// ForOwner keeps the source session id so the owner can see what was promoted.
func (p Provenance) ForOwner() ProvenanceView {
	return ProvenanceView{
		SourceSessionID:     p.SourceSessionID,
		SourceOwnerID:       p.SourceOwnerID,
		PromotedFromPrivate: p.PromotedFromPrivate,
		RemovedByOwner:      p.RemovedByOwner,
	}
}

// ForTeammate drops the session id and says the fact came from a private session.
func (p Provenance) ForTeammate() ProvenanceView {
	return ProvenanceView{
		SourceOwnerID:       p.SourceOwnerID,
		PromotedFromPrivate: p.PromotedFromPrivate,
		Summary:             PrivateSessionSummary,
		RemovedByOwner:      p.RemovedByOwner,
	}
}

// FormatProvenancePrefix stores provenance in context_snippet. Postgres has
// no provenance columns (migrations/001). The prefix has no session title.
func FormatProvenancePrefix(p Provenance, existing string) string {
	rest := stripProvenancePrefix(existing)
	line := fmt.Sprintf("«provenance:source_session_id=%s;source_owner_id=%s;promoted_from_private=%t;removed_by_owner=%t»",
		sanitizeProvenanceID(p.SourceSessionID),
		sanitizeProvenanceID(p.SourceOwnerID),
		p.PromotedFromPrivate,
		p.RemovedByOwner,
	)
	if strings.TrimSpace(rest) == "" {
		return line
	}
	return line + "\n" + rest
}

// ParseProvenancePrefix reads a prefix written by FormatProvenancePrefix.
func ParseProvenancePrefix(s string) (Provenance, bool) {
	s = strings.TrimSpace(s)
	const marker = "«provenance:"
	if !strings.HasPrefix(s, marker) {
		return Provenance{}, false
	}
	end := strings.Index(s, "»")
	if end < 0 {
		return Provenance{}, false
	}
	var p Provenance
	for _, part := range strings.Split(s[len(marker):end], ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch k {
		case "source_session_id":
			p.SourceSessionID = v
		case "source_owner_id":
			p.SourceOwnerID = v
		case "promoted_from_private":
			p.PromotedFromPrivate = v == "true"
		case "removed_by_owner":
			p.RemovedByOwner = v == "true"
		}
	}
	return p, true
}

func stripProvenancePrefix(s string) string {
	s = strings.TrimSpace(s)
	const marker = "«provenance:"
	if !strings.HasPrefix(s, marker) {
		return s
	}
	end := strings.Index(s, "»")
	if end < 0 {
		return s
	}
	rest := s[end+len("»"):]
	rest = strings.TrimPrefix(rest, "\n")
	return strings.TrimSpace(rest)
}

func sanitizeProvenanceID(id string) string {
	id = strings.ReplaceAll(id, ";", "")
	id = strings.ReplaceAll(id, "»", "")
	id = strings.ReplaceAll(id, "\n", "")
	id = strings.ReplaceAll(id, "\r", "")
	return strings.TrimSpace(id)
}
