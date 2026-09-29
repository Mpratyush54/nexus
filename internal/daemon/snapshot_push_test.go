package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/store"
)

func TestPushSnapshotHTTP(t *testing.T) {
	var gotPath string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if body["project_id"] == "" {
			http.Error(w, "missing project", 400)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true,"snapshot_version":1}`))
	}))
	defer srv.Close()

	hs := NewHTTPMemoryStore(srv.URL, "tok", "proj-1")
	err := hs.PushSnapshot(t.Context(), &store.SessionSnapshot{
		SessionID:         "sess-1",
		Harness:           "antigravity",
		ConversationID:    "c1",
		TranscriptPayload: []byte("gz"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/sessions/sess-1/snapshot" {
		t.Fatalf("path=%q", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") {
		t.Fatalf("auth=%q", gotAuth)
	}
}

func TestPushSnapshotRetriesOnFailure(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			http.Error(w, "boom", 500)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	hs := NewHTTPMemoryStore(srv.URL, "tok", "proj")
	hs.HTTP = &http.Client{Timeout: time.Second}
	err := hs.PushSnapshot(t.Context(), &store.SessionSnapshot{
		SessionID: "s", TranscriptPayload: []byte("x"), ConversationID: "c", Harness: "antigravity",
	})
	if err == nil {
		t.Fatal("want first failure")
	}
	if err := hs.PushSnapshot(t.Context(), &store.SessionSnapshot{
		SessionID: "s", TranscriptPayload: []byte("x"), ConversationID: "c", Harness: "antigravity",
	}); err != nil {
		t.Fatal(err)
	}
}
