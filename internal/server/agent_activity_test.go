package server

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestActivityUsesOriginalDateAndExcludesUploadsAndOtherOwners(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	token := loginAs(t, s, "owner")
	project := resolveTestProject(t, s, token, "source-calendar")
	session, err := mem.UpsertAgentSession(t.Context(), &store.AgentSession{ProjectID: project, OwnerUserID: "owner", Harness: "codex", NativeID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -10).Add(21 * time.Hour)
	for _, turn := range []map[string]any{
		{"idx": 1, "role": "user", "text_preview": "Original prompt", "source_ts": at},
		{"idx": 2, "role": "assistant", "text_preview": "Answer", "source_ts": at},
		{"idx": 3, "role": "assistant", "text_preview": "No original date"},
	} {
		rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+session.ID+"/turns", token, turn)
		if rec.Code != http.StatusCreated {
			t.Fatalf("turn: %d %s", rec.Code, rec.Body.String())
		}
	}
	// Replays repair original dates without adding a second message.
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+session.ID+"/turns/batch", token, map[string]any{"turns": []map[string]any{
		{"idx": 3, "role": "assistant", "source_ts": at},
		{"idx": 4, "role": "user", "text_preview": "Still undated"},
	}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body.String())
	}
	other, _ := mem.UpsertAgentSession(t.Context(), &store.AgentSession{ProjectID: project, OwnerUserID: "other", Harness: "codex", NativeID: "private"})
	_ = mem.AppendSessionTurn(t.Context(), store.SessionTurn{SessionID: other.ID, Idx: 1, Role: "user", SourceAt: &at})
	_ = mem.AppendEvent(t.Context(), &store.Event{ProjectID: project, UserID: "owner", EventType: "SESSION_UPLOADED", CreatedAt: time.Now()})
	rec = doJSON(t, s, http.MethodGet, "/projects/"+project+"/dashboard?utc_offset_minutes=330", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Agent struct {
			Total   int `json:"total"`
			Undated int `json:"undated_messages"`
			Days    []struct {
				Date  string `json:"date"`
				Count int    `json:"count"`
			} `json:"days"`
		} `json:"agent_activity"`
	}
	decodeBody(t, rec, &body)
	if body.Agent.Total != 3 || body.Agent.Undated != 1 {
		t.Fatalf("activity = %+v", body.Agent)
	}
	expected := at.Add(330 * time.Minute).Format("2006-01-02")
	for _, day := range body.Agent.Days {
		if day.Date == expected && day.Count != 3 {
			t.Fatalf("source day = %+v", day)
		}
		if day.Date != expected && day.Count != 0 {
			t.Fatalf("receipt day leaked: %+v", day)
		}
	}
}

func TestArchivedCalendarRecoversDateWithoutDoubleCountingMirroredTurns(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	token := loginAs(t, s, "owner")
	project := resolveTestProject(t, s, token, "archive-calendar")
	at := time.Now().UTC().AddDate(0, 0, -30).Truncate(time.Second)
	session, _ := mem.UpsertAgentSession(t.Context(), &store.AgentSession{ProjectID: project, OwnerUserID: "owner", Harness: "cursor", NativeID: "native-old"})
	_ = mem.AppendSessionTurn(t.Context(), store.SessionTurn{SessionID: session.ID, Idx: 1, Role: "user", TextPreview: "Original prompt", SourceAt: &at})
	for _, id := range []string{"6f7d0a54-1111-4c2e-9c48-8a2b1f4e6d10", "6f7d0a54-1111-4c2e-9c48-8a2b1f4e6d11"} {
		err := mem.UpsertSessionSnapshot(t.Context(), &store.SessionSnapshot{SessionID: id, ProjectID: project, OwnerUserID: "owner", Harness: "cursor", ConversationID: "native-old", TurnCount: 1,
			TranscriptPayload: gzipJSONL(t, fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"role":"user","content":"Original prompt"}}`, at.Format(time.RFC3339)))})
		if err != nil {
			t.Fatal(err)
		}
	}
	// New work on the same day must add activity, not be lost to max(count).
	later := at.Add(time.Second)
	_ = mem.AppendSessionTurn(t.Context(), store.SessionTurn{SessionID: session.ID, Idx: 2, Role: "user", TextPreview: "New prompt", SourceAt: &later})
	rec := doJSON(t, s, http.MethodGet, "/projects/"+project+"/dashboard", token, nil)
	var body struct {
		Activity struct {
			Total int `json:"total"`
		} `json:"agent_activity"`
	}
	decodeBody(t, rec, &body)
	if rec.Code != http.StatusOK || body.Activity.Total != 2 {
		t.Fatalf("archive doubled: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSourceTimeUnderstandsHarnessEnvelopesAndRejectsInvalidDates(t *testing.T) {
	at := time.Now().UTC().AddDate(0, 0, -2).Truncate(time.Second)
	for _, entry := range []map[string]any{
		{"timestamp": at.Format(time.RFC3339)},
		{"info": map[string]any{"time": map[string]any{"created": float64(at.UnixMilli())}}},
		{"message": map[string]any{"timestamp": at.Format(time.RFC3339)}},
	} {
		got := transcriptSourceTime(entry)
		if got == nil || !got.Equal(at) {
			t.Fatalf("source %v = %v", entry, got)
		}
	}
	for _, entry := range []map[string]any{{}, {"timestamp": "not-a-date"}, {"timestamp": float64(0)}, {"timestamp": time.Now().AddDate(1, 0, 0).Format(time.RFC3339)}} {
		if got := transcriptSourceTime(entry); got != nil {
			t.Fatalf("invented date: %v", got)
		}
	}
}
