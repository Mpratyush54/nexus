package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestProjectEventsListAndFilters(t *testing.T) {
	s := newTestServer()
	token := loginAs(t, s, "events-user")
	projectID := resolveTestProject(t, s, token, "events-proj")

	rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID,
		"key":        "testing/events",
		"content":    "Creating a memory should append MEMORY_PROPOSED to the activity feed.",
		"level":      "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	decodeBody(t, rec, &created)
	id, _ := created["id"].(string)

	rec = doJSON(t, s, http.MethodPost, "/memory/"+id+"/confirm", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+projectID+"/events", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("events = %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []map[string]any `json:"items"`
		Count int              `json:"count"`
	}
	decodeBody(t, rec, &list)
	if list.Count < 2 {
		t.Fatalf("expected proposed+confirmed events, got %+v", list)
	}
	foundProposed, foundConfirmed := false, false
	for _, ev := range list.Items {
		switch ev["event_type"] {
		case "MEMORY_PROPOSED":
			foundProposed = true
		case "MEMORY_CONFIRMED":
			foundConfirmed = true
		}
	}
	if !foundProposed || !foundConfirmed {
		t.Fatalf("missing lifecycle events: proposed=%v confirmed=%v items=%+v", foundProposed, foundConfirmed, list.Items)
	}

	rec = doJSON(t, s, http.MethodGet, "/projects/"+projectID+"/events?type=MEMORY_CONFIRMED", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("filter = %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &list)
	if list.Count == 0 {
		t.Fatal("expected MEMORY_CONFIRMED filter to return rows")
	}
	for _, ev := range list.Items {
		if ev["event_type"] != "MEMORY_CONFIRMED" {
			t.Fatalf("type filter leaked %v", ev["event_type"])
		}
	}

	bob := loginAs(t, s, "events-bob")
	rec = doJSON(t, s, http.MethodGet, "/projects/"+projectID+"/events", bob, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-member events = %d, want 403", rec.Code)
	}
}

func TestNotificationDevicesAndPreferences(t *testing.T) {
	s := newTestServer()
	alice := loginAs(t, s, "dev-alice")
	proj := resolveTestProject(t, s, alice, "dev-proj")

	rec := doJSON(t, s, http.MethodPost, "/memory", alice, map[string]any{
		"project_id": proj,
		"key":        "testing/notif-pref",
		"content":    "Proposed memory used to verify preference filtering on the inbox.",
		"level":      "project",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/notifications?project_id="+proj, alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbox = %d %s", rec.Code, rec.Body.String())
	}
	var inbox struct {
		Items []map[string]any `json:"items"`
		Count int              `json:"count"`
	}
	decodeBody(t, rec, &inbox)
	if inbox.Count == 0 {
		t.Fatal("expected inbox items after create")
	}

	rec = doJSON(t, s, http.MethodPut, "/notifications/preferences", alice, map[string]any{
		"types": map[string]bool{"MEMORY_PROPOSED": false},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("prefs put = %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, s, http.MethodGet, "/notifications?project_id="+proj, alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("inbox after pref = %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &inbox)
	for _, n := range inbox.Items {
		if n["type"] == "MEMORY_PROPOSED" {
			t.Fatal("MEMORY_PROPOSED should be hidden after preference disable")
		}
	}

	rec = doJSON(t, s, http.MethodPost, "/notifications/subscribe", alice, map[string]any{
		"endpoint": "https://push.example/sub/device-a",
		"keys":     map[string]string{"p256dh": "abc", "auth": "def"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe = %d %s", rec.Code, rec.Body.String())
	}
	var sub struct {
		ID     string `json:"id"`
		Device struct {
			ID   string `json:"id"`
			Host string `json:"host"`
		} `json:"device"`
	}
	decodeBody(t, rec, &sub)
	if sub.ID == "" || sub.Device.Host != "push.example" {
		t.Fatalf("subscribe device = %+v", sub)
	}

	rec = doJSON(t, s, http.MethodGet, "/notifications/devices", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("devices = %d %s", rec.Code, rec.Body.String())
	}
	var devices struct {
		Items []map[string]any `json:"items"`
		Count int              `json:"count"`
	}
	decodeBody(t, rec, &devices)
	if devices.Count != 1 {
		t.Fatalf("devices = %+v, want 1", devices)
	}
	body := rec.Body.String()
	if strings.Contains(body, "p256dh") || strings.Contains(body, "push.example/sub/device-a") {
		t.Fatalf("device list leaked keys or full endpoint: %s", body)
	}

	rec = doJSON(t, s, http.MethodDelete, "/notifications/devices/"+sub.ID, alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/notifications/devices", alice, nil)
	decodeBody(t, rec, &devices)
	if devices.Count != 0 {
		t.Fatalf("after revoke count = %d", devices.Count)
	}
}
