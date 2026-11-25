package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"central-memory/internal/desktopbridge"
	"central-memory/internal/store"
)

func TestDesktopImagesNeedAnUpdatedDesktopAndNeverLeakToBrowsers(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, e := mem.ResolveProject(ctx, "", "", "desktop-images")
	if e != nil {
		t.Fatal(e)
	}
	_, _ = mem.ClaimProject(ctx, project.ID, "owner")
	token := loginAs(t, s, "owner")
	created := doJSON(t, s, "POST", "/v1/agent-sessions", token, map[string]any{"project_id": project.ID, "harness": "claude", "native_id": "native"})
	var session store.AgentSession
	decodeBody(t, created, &session)
	call := func(method, path, key string, protocol int, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("X-Nexus-Desktop-Key", key)
		}
		if protocol > 0 {
			r.Header.Set(desktopbridge.ProtocolHeader, strconv.Itoa(protocol))
		}
		out := httptest.NewRecorder()
		s.Handler().ServeHTTP(out, r)
		return out
	}
	device := "desktop-images-0123456789"
	secret := strings.Repeat("k", 64)
	registration := map[string]any{"id": device, "name": "Laptop", "projects": []string{project.ID}, "harnesses": []string{"claude"}}
	// An older desktop registers without the protocol header.
	if got := call("POST", "/v1/desktop/devices", secret, 0, registration); got.Code != 200 {
		t.Fatalf("register: %d %s", got.Code, got.Body)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	image := desktopbridge.Attachment{Name: "screen.png", MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(png)}
	req := desktopbridge.Request{ID: "img-1", DeviceID: device, ProjectID: project.ID, SessionID: session.ID, Kind: "turn", Agent: "claude", Prompt: "what is this?", Attachments: []desktopbridge.Attachment{image}}
	got := call("POST", "/v1/desktop/jobs", "", 0, req)
	if got.Code != 409 || !strings.Contains(got.Body.String(), "Update Nexus on your computer to send images") {
		t.Fatalf("old desktop must refuse images clearly: %d %s", got.Code, got.Body)
	}
	// Text still works for the old desktop.
	text := req
	text.ID, text.Attachments = "text-1", nil
	if got := call("POST", "/v1/desktop/jobs", "", 0, text); got.Code != 202 {
		t.Fatalf("text turn: %d %s", got.Code, got.Body)
	}
	_ = call("POST", "/v1/desktop/jobs/text-1/cancel", "", 0, nil)
	// The updated desktop reports protocol 2 on its heartbeat.
	if got := call("POST", "/v1/desktop/devices", secret, desktopbridge.ProtocolVersion, registration); got.Code != 200 {
		t.Fatalf("re-register: %d %s", got.Code, got.Body)
	}
	if got := call("GET", "/v1/desktop/devices", "", 0, nil); !strings.Contains(got.Body.String(), `"protocol":2`) {
		t.Fatalf("browsers should see the desktop's protocol: %s", got.Body)
	}
	bad := req
	bad.ID = "img-bad"
	bad.Attachments = []desktopbridge.Attachment{{Name: "x.png", MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(append(png, make([]byte, desktopbridge.MaxAttachmentBytes)...))}}
	if got := call("POST", "/v1/desktop/jobs", "", 0, bad); got.Code != 400 || !strings.Contains(got.Body.String(), "5 MB") {
		t.Fatalf("oversized image: %d %s", got.Code, got.Body)
	}
	got = call("POST", "/v1/desktop/jobs", "", 0, req)
	if got.Code != 202 {
		t.Fatalf("image turn: %d %s", got.Code, got.Body)
	}
	if strings.Contains(got.Body.String(), image.Data) || !strings.Contains(got.Body.String(), `"sha256"`) {
		t.Fatalf("create response must carry metadata only: %s", got.Body)
	}
	if got := call("GET", "/v1/desktop/jobs/img-1", "", 0, nil); strings.Contains(got.Body.String(), image.Data) {
		t.Fatal("status read leaked image bytes")
	}
	claim := call("POST", "/v1/desktop/devices/"+device+"/claim", secret, desktopbridge.ProtocolVersion, nil)
	var claimed struct {
		Job desktopbridge.Job `json:"job"`
	}
	decodeBody(t, claim, &claimed)
	if claim.Code != 200 || len(claimed.Job.Attachments) != 1 || claimed.Job.Attachments[0].Data != image.Data {
		t.Fatalf("worker claim must carry the image: %d %s", claim.Code, claim.Body)
	}
}
