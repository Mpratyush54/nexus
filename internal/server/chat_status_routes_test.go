package server

import (
	"net/http"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestChatStatusReportListAndRead(t *testing.T) {
	s := newTestServer()
	alice, bob := loginAs(t, s, "alice"), loginAs(t, s, "bob")
	put := func(tok string, items ...store.ChatLiveReport) []string {
		t.Helper()
		rec := doJSON(t, s, http.MethodPut, "/v1/chat-status", tok, map[string]any{"items": items})
		if rec.Code != http.StatusOK {
			t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Read []string `json:"read"`
		}
		decodeBody(t, rec, &out)
		return out.Read
	}
	list := func(tok string) []store.ChatLiveStatus {
		t.Helper()
		rec := doJSON(t, s, http.MethodGet, "/v1/chat-status", tok, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Items []store.ChatLiveStatus `json:"items"`
		}
		decodeBody(t, rec, &out)
		return out.Items
	}

	put(alice, store.ChatLiveReport{SessionID: "chat-1", Status: "running"}, store.ChatLiveReport{SessionID: "chat-2", Status: "done", Unread: true})
	if got := list(alice); len(got) != 2 || got[0].Status != "running" || !got[1].Unread {
		t.Fatalf("alice's list %+v", got)
	}
	if got := list(bob); len(got) != 0 {
		t.Fatalf("bob sees alice's statuses: %+v", got)
	}
	if rec := doJSON(t, s, http.MethodPut, "/v1/chat-status", alice, map[string]any{"items": []map[string]any{{"session_id": "x", "status": "rm -rf"}}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status: %d", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodGet, "/v1/chat-status", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d", rec.Code)
	}

	// Opened on the web: the desktop's next report learns it was read.
	time.Sleep(2 * time.Millisecond)
	if rec := doJSON(t, s, http.MethodPost, "/v1/chat-status/chat-2/read", alice, nil); rec.Code != http.StatusOK {
		t.Fatalf("read: %d %s", rec.Code, rec.Body.String())
	}
	if got := list(alice); len(got) != 2 || got[1].Unread {
		t.Fatalf("after read %+v", got)
	}
	if read := put(alice, store.ChatLiveReport{SessionID: "chat-2", Status: "done", Unread: true}); len(read) != 1 || read[0] != "chat-2" {
		t.Fatalf("read back = %v", read)
	}
	put(alice, store.ChatLiveReport{SessionID: "chat-1", Status: "idle"}, store.ChatLiveReport{SessionID: "chat-2", Status: "idle"})
	if got := list(alice); len(got) != 0 {
		t.Fatalf("idle chats listed: %+v", got)
	}
}
