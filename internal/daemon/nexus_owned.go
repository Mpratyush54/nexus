package daemon

import "central-memory/internal/owned"

// Chats that run inside Nexus are saved by Nexus itself (continue.record);
// capture skips their native transcripts. The registry lives in the leaf
// package internal/owned so the desktop core can mark sessions too.

// MarkNexusOwned records that Nexus started (and saves) this agent session.
func MarkNexusOwned(nativeID string) { owned.Mark(nativeID) }

// IsNexusOwnedSession reports whether a native session id belongs to Nexus.
func IsNexusOwnedSession(nativeID string) bool { return owned.IsSession(nativeID) }

// isNexusOwnedTranscript reports whether a transcript path belongs to a chat
// Nexus already saves.
func isNexusOwnedTranscript(path string) bool { return owned.IsTranscript(path) }
