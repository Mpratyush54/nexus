package server

import (
	"encoding/json"
	"strings"
	"testing"

	"central-memory/internal/memoryact"
	"central-memory/internal/store"
)

func TestMemoryPin(t *testing.T) {
	s := newTestServer()
	s.registerMemoryActionRoutes()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "mem-pin")
	item := seedSessionMemory(t, s, "alice", projectID, "sess-pin", "Pin this deploy fact so the pool size stays visible to the team.")

	if rec := doJSON(t, s, "POST", "/memory/"+item.ID+"/pin", "", nil); rec.Code != 401 {
		t.Fatalf("unauth pin = %d, want 401", rec.Code)
	}
	stranger := loginAs(t, s, "stranger")
	if rec := doJSON(t, s, "POST", "/memory/"+item.ID+"/pin", stranger, nil); rec.Code != 403 {
		t.Fatalf("non-member pin = %d, want 403", rec.Code)
	}

	rec := doJSON(t, s, "POST", "/memory/"+item.ID+"/pin", token, nil)
	if rec.Code != 200 {
		t.Fatalf("pin status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodePublicMemory(t, rec.Body.Bytes())
	assertPublicMemoryStatus(t, body.Status, memoryact.PublicActive)
	if !body.Pinned {
		t.Fatalf("pinned = false, body = %s", rec.Body.String())
	}
	if !containsTag(body.Tags, "pinned") {
		t.Fatalf("tags = %#v, want pinned", body.Tags)
	}
	if !store.MemoryPinned(item.ID) {
		t.Fatal("MemStore pin flag was not set")
	}
	if strings.Contains(rec.Body.String(), "PROPOSED") || strings.Contains(rec.Body.String(), "CONFIRMED") {
		t.Fatalf("response leaked legacy status: %s", rec.Body.String())
	}
}

func TestMemoryScope(t *testing.T) {
	s := newTestServer()
	s.registerMemoryActionRoutes()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "mem-scope")
	item := seedSessionMemory(t, s, "alice", projectID, "sess-scope", "Scope this fact from the session into the project conventions list.")

	rec := doJSON(t, s, "POST", "/memory/"+item.ID+"/scope", token, map[string]string{"level": "project"})
	if rec.Code != 200 {
		t.Fatalf("scope status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodePublicMemory(t, rec.Body.Bytes())
	if body.Level != "project" {
		t.Fatalf("level = %q, want project", body.Level)
	}
	assertPublicMemoryStatus(t, body.Status, memoryact.PublicActive)

	rec = doJSON(t, s, "POST", "/memory/"+item.ID+"/scope", token, map[string]string{"level": "Team"})
	if rec.Code != 200 {
		t.Fatalf("team scope status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body = decodePublicMemory(t, rec.Body.Bytes())
	if body.Level != "organization" {
		t.Fatalf("level = %q, want organization", body.Level)
	}
	assertPublicMemoryStatus(t, body.Status, memoryact.PublicActive)

	rec = doJSON(t, s, "POST", "/memory/"+item.ID+"/scope", token, map[string]string{"level": "galaxy"})
	if rec.Code != 400 {
		t.Fatalf("unknown level = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestMemoryForget(t *testing.T) {
	s := newTestServer()
	s.registerMemoryActionRoutes()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "mem-forget")
	item := seedSessionMemory(t, s, "alice", projectID, "sess-forget", "Forget this scratch fact once the migration notes are captured.")

	rec := doJSON(t, s, "POST", "/memory/"+item.ID+"/forget", token, nil)
	if rec.Code != 200 {
		t.Fatalf("forget status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodePublicMemory(t, rec.Body.Bytes())
	assertPublicMemoryStatus(t, body.Status, memoryact.PublicForgotten)
	if strings.Contains(rec.Body.String(), "PROPOSED") || strings.Contains(rec.Body.String(), "CONFIRMED") {
		t.Fatalf("response leaked legacy status: %s", rec.Body.String())
	}

	stored, err := s.Store.(*store.MemStore).GetMemoryItem(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "forgotten" {
		t.Fatalf("stored status = %q, want forgotten", stored.Status)
	}
}

func TestMemoryPromote(t *testing.T) {
	s := newTestServer()
	s.registerMemoryActionRoutes()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "mem-promote")

	emailItem := seedSessionMemory(t, s, "alice", projectID, "sess-email",
		"Email the on-call at ada@example.com before changing the connection pool size.")
	rec := doJSON(t, s, "POST", "/memory/"+emailItem.ID+"/auto-promote", token, nil)
	if rec.Code != 200 {
		t.Fatalf("promote status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodePublicMemory(t, rec.Body.Bytes())
	assertPublicMemoryStatus(t, body.Status, memoryact.PublicActive)
	if body.Held {
		t.Fatal("email fact was held")
	}
	if body.Level != "project" {
		t.Fatalf("level = %q, want project", body.Level)
	}
	if strings.Contains(body.Content, "ada@example.com") || !strings.Contains(body.Content, memoryact.RedactedEmail) {
		t.Fatalf("content = %q, want email redacted", body.Content)
	}
	if body.Provenance == nil || body.Provenance.SourceSessionID != "sess-email" || !body.Provenance.PromotedFromPrivate {
		t.Fatalf("owner provenance = %+v", body.Provenance)
	}
	if body.SessionID != "" {
		t.Fatalf("promoted item still exposes session_id %q", body.SessionID)
	}
	if strings.Contains(rec.Body.String(), "PROPOSED") || strings.Contains(rec.Body.String(), "CONFIRMED") || strings.Contains(rec.Body.String(), "title") {
		t.Fatalf("response leaked legacy status or a title: %s", rec.Body.String())
	}

	bob := loginAs(t, s, "bob")
	if err := s.Store.(*store.MemStore).GrantMember(t.Context(), projectID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	other := seedSessionMemory(t, s, "alice", projectID, "sess-bob-view",
		"Email the reviewer at bob.reader@example.com before merging the pool patch.")
	rec = doJSON(t, s, "POST", "/memory/"+other.ID+"/auto-promote", bob, nil)
	if rec.Code != 200 {
		t.Fatalf("teammate promote = %d, body = %s", rec.Code, rec.Body.String())
	}
	team := decodePublicMemory(t, rec.Body.Bytes())
	if team.Provenance == nil || team.Provenance.SourceSessionID != "" || team.Provenance.Summary != memoryact.PrivateSessionSummary {
		t.Fatalf("teammate provenance = %+v", team.Provenance)
	}
	if strings.Contains(rec.Body.String(), "sess-bob-view") {
		t.Fatalf("teammate response leaked session id: %s", rec.Body.String())
	}

	heldItem := seedSessionMemory(t, s, "alice", projectID, "sess-secret",
		"Staging notes included AKIAIOSFODNN7EXAMPLE for the old deploy user account.")
	rec = doJSON(t, s, "POST", "/memory/"+heldItem.ID+"/auto-promote", token, nil)
	if rec.Code != 200 {
		t.Fatalf("hold promote = %d, body = %s", rec.Code, rec.Body.String())
	}
	held := decodePublicMemory(t, rec.Body.Bytes())
	assertPublicMemoryStatus(t, held.Status, memoryact.PublicActive)
	if !held.Held {
		t.Fatal("AKIA fact was not held")
	}
	if held.Level != "session" {
		t.Fatalf("held level = %q, want session", held.Level)
	}
	if !strings.Contains(held.Content, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("held content dropped the key: %q", held.Content)
	}
	stored, err := s.Store.(*store.MemStore).GetMemoryItem(t.Context(), heldItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Level != "session" {
		t.Fatalf("stored level = %q, want session", stored.Level)
	}
}

func TestMemoryRemove(t *testing.T) {
	s := newTestServer()
	s.registerMemoryActionRoutes()
	token := loginAs(t, s, "alice")
	projectID := resolveTestProject(t, s, token, "mem-remove")
	if err := s.Store.(*store.MemStore).GrantMember(t.Context(), projectID, "bob", "alice"); err != nil {
		t.Fatal(err)
	}
	item := seedSessionMemory(t, s, "alice", projectID, "sess-remove",
		"The project pins pgx to v5.6 because the pool leaked under load.")
	rec := doJSON(t, s, "POST", "/memory/"+item.ID+"/auto-promote", token, nil)
	if rec.Code != 200 {
		t.Fatalf("promote before remove = %d, body = %s", rec.Code, rec.Body.String())
	}

	bob := loginAs(t, s, "bob")
	rec = doJSON(t, s, "POST", "/memory/"+item.ID+"/remove-promoted", bob, nil)
	if rec.Code != 403 {
		t.Fatalf("non-owner remove = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, "POST", "/memory/"+item.ID+"/remove-promoted", token, nil)
	if rec.Code != 200 {
		t.Fatalf("owner remove = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodePublicMemory(t, rec.Body.Bytes())
	assertPublicMemoryStatus(t, body.Status, memoryact.PublicForgotten)
	if !body.RemovedByOwner {
		t.Fatalf("removed_by_owner missing: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "PROPOSED") || strings.Contains(rec.Body.String(), "CONFIRMED") {
		t.Fatalf("response leaked legacy status: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"title"`) || strings.Contains(rec.Body.String(), "session_title") || strings.Contains(rec.Body.String(), "sess-remove") {
		t.Fatalf("removal response included a session title or session id: %s", rec.Body.String())
	}
	stored, err := s.Store.(*store.MemStore).GetMemoryItem(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "forgotten" {
		t.Fatalf("stored status = %q, want forgotten", stored.Status)
	}

	events, err := s.Store.(*store.MemStore).ListAuditEvents(t.Context(), projectID, "memory.removed_by_owner", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(events))
	}
	if events[0].Metadata["removed_by_owner"] != true {
		t.Fatalf("audit metadata = %#v", events[0].Metadata)
	}
	for k := range events[0].Metadata {
		if k == "title" || k == "session_title" || k == "session_id" || k == "source_session_id" {
			t.Fatalf("audit metadata key %q", k)
		}
	}
}

func seedSessionMemory(t *testing.T, s *Server, owner, projectID, sessionID, content string) *store.MemoryItem {
	t.Helper()
	item := &store.MemoryItem{
		ProjectID:  projectID,
		UserID:     owner,
		SessionID:  sessionID,
		Key:        "fact/learned",
		Content:    content,
		Level:      "session",
		Scope:      "fact",
		Status:     "PROPOSED",
		Confidence: 0.9,
	}
	if err := s.Store.(*store.MemStore).CreateMemoryItem(t.Context(), item); err != nil {
		t.Fatalf("CreateMemoryItem: %v", err)
	}
	return item
}

type publicMemoryBody struct {
	ID             string                    `json:"id"`
	Status         string                    `json:"status"`
	Level          string                    `json:"level"`
	Content        string                    `json:"content"`
	SessionID      string                    `json:"session_id"`
	Tags           []string                  `json:"tags"`
	Pinned         bool                      `json:"pinned"`
	Held           bool                      `json:"held"`
	RemovedByOwner bool                      `json:"removed_by_owner"`
	Provenance     *memoryact.ProvenanceView `json:"provenance"`
}

func decodePublicMemory(t *testing.T, raw []byte) publicMemoryBody {
	t.Helper()
	var body publicMemoryBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return body
}

func assertPublicMemoryStatus(t *testing.T, got, want string) {
	t.Helper()
	if got == "PROPOSED" || got == "CONFIRMED" {
		t.Fatalf("public status leaked %q", got)
	}
	if got != want {
		t.Fatalf("status = %q, want %q", got, want)
	}
}

func containsTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
