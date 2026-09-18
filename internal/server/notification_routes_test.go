package server

import (
	"net/http"
	"testing"
)

func TestNotificationListRequiresProject(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	rec := doJSON(t, s, http.MethodGet, "/notifications", alice, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationListAndVapid(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "alice")
	proj := resolveTestProject(t, s, alice, "notif-proj")

	rec := doJSON(t, s, http.MethodGet, "/notifications?project_id="+proj, alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/notifications/vapid-public-key", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("vapid = %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	decodeBody(t, rec, &out)
	if out["public_key"] == nil || out["public_key"] == "" {
		t.Fatalf("expected public_key: %+v", out)
	}

	rec = doJSON(t, s, http.MethodPost, "/notifications/subscribe", alice, map[string]any{
		"endpoint": "https://push.example/sub/1",
		"keys":     map[string]string{"p256dh": "abc", "auth": "def"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe = %d body=%s", rec.Code, rec.Body.String())
	}
}
