package daemon

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"central-memory/internal/store"
)

func TestMirrorAgentSessionTurnsUsesCloudContract(t *testing.T) {
	var seen []string
	var gotTurn map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/agent-sessions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["project_id"] != "project-1" || body["harness"] != "codex" || body["native_id"] != "native-1" {
				http.Error(w, "bad session identity", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "cloud-session"})
		case "/v1/agent-sessions/cloud-session/turns":
			_ = json.NewDecoder(r.Body).Decode(&gotTurn)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	hs := NewHTTPMemoryStore(srv.URL, "test-token", "project-1")
	id, err := hs.MirrorAgentSessionTurns(t.Context(), "codex", "native-1", "machine-1", "C:/work", []MirroredTurn{{
		Idx: 170000000000001, Role: "user", TextPreview: "ship the bridge",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if id != "cloud-session" {
		t.Fatalf("session id = %q", id)
	}
	if gotTurn["idx"] != float64(170000000000001) || gotTurn["role"] != "user" || gotTurn["text_preview"] != "ship the bridge" {
		t.Fatalf("turn = %#v", gotTurn)
	}
	if got := strings.Join(seen, ","); got != "POST /v1/agent-sessions,POST /v1/agent-sessions/cloud-session/turns" {
		t.Fatalf("calls = %s", got)
	}
}

func TestPublishAgentSessionVersionUploadsTranscriptAndCompletes(t *testing.T) {
	plain := []byte(`{"role":"user","content":"keep this decision"}` + "\n")
	gz, err := gzipBytes(plain)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plain)
	hash := hex.EncodeToString(sum[:])
	var complete map[string]any
	var blobBody map[string]any
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/agent-sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "cloud-session"})
		case "/v1/blobs/" + hash:
			_ = json.NewDecoder(r.Body).Decode(&blobBody)
			w.WriteHeader(http.StatusCreated)
		case "/v1/agent-sessions/cloud-session/versions":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": 3})
		case "/v1/agent-sessions/cloud-session/versions/3/complete":
			_ = json.NewDecoder(r.Body).Decode(&complete)
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "complete"})
		default:
			body, _ := io.ReadAll(r.Body)
			http.Error(w, "unexpected "+r.URL.Path+" "+string(body), http.StatusNotFound)
		}
	}))
	defer srv.Close()

	hs := NewHTTPMemoryStore(srv.URL, "test-token", "project-1")
	err = hs.PublishAgentSessionVersion(t.Context(), &store.SessionSnapshot{
		Harness: "codex", ConversationID: "native-1", SourceMachineID: "machine-1", TranscriptPayload: gz,
	}, "C:/work")
	if err != nil {
		t.Fatal(err)
	}
	if blobBody["project_id"] != "project-1" || blobBody["purpose"] != "transcript" {
		t.Fatalf("blob body = %#v", blobBody)
	}
	encoded, _ := blobBody["body_b64"].(string)
	if got, err := base64.StdEncoding.DecodeString(encoded); err != nil || string(got) != string(plain) {
		t.Fatalf("uploaded transcript = %q, %v", got, err)
	}
	transcript, _ := complete["transcript"].(map[string]any)
	if transcript["blob"] != "sha256:"+hash || complete["native_id"] != "native-1" {
		t.Fatalf("manifest = %#v", complete)
	}
	want := "POST /v1/agent-sessions,PUT /v1/blobs/" + hash + ",POST /v1/agent-sessions/cloud-session/versions,POST /v1/agent-sessions/cloud-session/versions/3/complete"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls = %s\nwant = %s", got, want)
	}
}
